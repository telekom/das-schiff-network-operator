# das-schiff-cra-grout.Dockerfile
#
# CRA container image for the `cra-grout` flavor: grout (DPDK graph router fast
# path) + FRR (control plane, with the grout zebra dataplane plugin) + grcli +
# the grout-cra sidecar (mTLS HTTP -> grcli/FRR apply). This is the open-source
# DPDK analog of das-schiff-cra-vsr, and mirrors the systemd-init structure of
# das-schiff-cra-frr.Dockerfile.
#
# The container is started by the node-setup scripts inside the CRA network
# namespace (`nerdctl --network=ns:/var/run/netns/cra run ...`); grout creates
# the fabric `hbn` uplink as a DPDK port and the netdev is moved to the host
# side to be wired to the fabric (see e2e/images/kind-node/cra grout variant).
#
# NOTE: the grout artifacts (grout, grcli, the FRR `dplane_grout.so` zebra
# plugin) are pulled from the upstream grout image (ARG GROUT_IMAGE). The exact
# in-image paths below track quay.io/grout/grout:edge and MUST be validated on a
# real build/host (Phase B). For a production fast path, prepare hugepages +
# vfio-pci on the node and clear grout's `-t` test-mode flag (docker/grout.env).

ARG GO_VERSION=1.25
ARG GROUT_IMAGE=quay.io/grout/grout:edge

# ---- Stage 1: build the grout-cra sidecar (pure Go, no cgo/DPDK) ------------
FROM docker.io/library/golang:${GO_VERSION}-alpine AS builder

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/grout-cra/main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

ARG ldflags
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "${ldflags}" -a -o grout-cra main.go

# ---- Stage 2: runtime image based on the grout image itself ----------------
# We base the CRA runtime directly on grout:edge (CentOS Stream 10). That image
# already ships, ABI-consistent, everything the CRA needs: grout + grcli (with
# their EL10 shared libs: libedit, libecoli, librte_*), the *patched* FRR
# (zebra/bgpd/staticd/mgmtd under /usr/libexec/frr + the matching
# dplane_grout.so), python3 and systemd. Layering the grout/grcli binaries onto
# a foreign distro (e.g. ubuntu) breaks at runtime with missing shared libraries
# (`grcli: libedit.so.0: cannot open shared object file`) and risks an ABI
# mismatch between an apt-installed FRR and the grout-built dplane_grout.so. The
# base image already provides grout.service and frr.service; we add the
# grout-cra sidecar, override the grout unit for the CRA (test-mode + socket
# perms) and enable the daemons.
FROM ${GROUT_IMAGE}

# grout-cra sidecar (static Go, distro-independent).
COPY --from=builder /workspace/grout-cra /usr/local/bin/grout-cra

# Stable paths for our unit files (grout/grcli live in /usr/bin in the base).
RUN ln -sf /usr/bin/grout /usr/local/bin/grout \
 && ln -sf /usr/bin/grcli /usr/local/bin/grcli

# systemd units + config. Our grout.service overrides the base image's default
# (adds `-t -m 666` via /etc/default/grout and drops its /etc/grout.init
# ExecStartPost); grout-cra.service is new. Units go in /etc/systemd/system so
# they take precedence over the base image's /usr/lib/systemd/system copies.
COPY ./docker/grout.service /etc/systemd/system/grout.service
COPY ./docker/grout-cra.service /etc/systemd/system/grout-cra.service
COPY ./docker/grout.env /etc/default/grout
COPY ./docker/grout-cra.env /etc/default/grout-cra
COPY ./docker/grout-wait-ready /usr/local/bin/grout-wait-ready
COPY ./docker/daemons /etc/frr/daemons
COPY ./docker/hosts /etc/hosts

# grout runs inside the CRA netns; keep the upstream guidance for running grout
# in a private netns/container (don't inherit a bogus default route, relax
# strict rp_filter on the control-plane taps).
RUN printf '\nGROUT_OVERRIDE_DEFAULT_ROUTE=true\nGROUT_OVERRIDE_RP_FILTER=true\n' >> /etc/default/grout

RUN chmod +x /usr/local/bin/grout-wait-ready

# zebra must load the grout dataplane plugin so FRR programs grout's FIB (the
# base image already sets this, but re-assert it in case the daemons file above
# reset it).
RUN sed -i 's/^zebra_options=.*/zebra_options="  -A 127.0.0.1 -s 90000000 -M dplane_grout"/' /etc/frr/daemons || \
    echo 'zebra_options="  -A 127.0.0.1 -s 90000000 -M dplane_grout"' >> /etc/frr/daemons

# udevd cannot run in a container; make udevadm a no-op so any `settle` call
# returns immediately (systemd-udevd.service failing is harmless).
RUN rm -f /usr/bin/udevadm && ln -sf /usr/bin/true /usr/bin/udevadm

# FRR config must exist and be frr-owned for frrinit.sh to start the daemons;
# grout-cra rewrites frr.conf at ApplyConfiguration time.
RUN mkdir -p /etc/cra \
 && touch /etc/frr/frr.conf /etc/frr/vtysh.conf \
 && chown -R frr:frr /etc/frr || true

RUN systemctl enable grout.service grout-cra.service frr.service

# The base grout:edge image sets ENTRYPOINT to catatonit, which stays PID 1 and
# runs its argument as a child. systemd MUST be PID 1 (it needs to own the cgroup
# and reap init), so override the entrypoint to exec systemd directly.
ENTRYPOINT []
CMD ["/sbin/init"]
