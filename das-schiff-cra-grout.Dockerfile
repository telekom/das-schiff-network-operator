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

# GROUT_IMAGE must be newer than v0.16.3: BGP unnumbered over grout ports needs
# DPDK/grout#658 ("ip6: punt received router advertisements to control plane",
# merged 2026-08-05), which fixes DPDK/grout#657 -- before it grout dropped
# received IPv6 Router Advertisements as unsupported, so FRR could never learn
# the peer's link-local next-hop and `neighbor <iface> interface remote-as ...`
# stayed in Idle. That PR also carries the zebra patch (kernel_neigh_update
# bypassing the dplane) in grout's own FRR build, which is a second reason to
# take FRR from this image rather than from a distro package.
#
# The edge image is also what carries the fix for IPv4-mapped EVPN VTEP
# gateways: on v0.16.3, a VTEP that advertises only IPv6 prefixes is cached
# under a key the next-hop lookup never uses and silently blackholes half of an
# ECMP pair (see pkg/cra-grout/README.md).
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
# in a private netns/container (don't inherit a bogus default route).
#
# rp_filter is deliberately left to us rather than to grout. GROUT_OVERRIDE_RP_FILTER
# pins every control-plane representor to mode 2 (loose), which is not enough
# here: the underlay is ECMP, so a session sourced from the VTEP goes out one
# uplink and its answer comes back on the other, and the kernel dropped every
# such reply (TcpExtIPReversePathFilter) -- the EVPN sessions to the route
# reflectors never left Connect. The effective mode is max(all, <iface>), so the
# override cannot be relaxed from the outside; disable it and turn reverse path
# filtering off for the whole CRA namespace instead, as grout's own EVPN tests do.
RUN printf '\nGROUT_OVERRIDE_DEFAULT_ROUTE=true\nGROUT_OVERRIDE_RP_FILTER=false\n' >> /etc/default/grout

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

# The base image ships /usr/lib/systemd/system/frr.service.d/grout.conf, which
# runs FRR with PrivateNetwork=true + JoinsNamespaceOf=grout.service. That pairs
# with upstream's grout.service, which is itself PrivateNetwork=true: the two
# share one private netns. Our grout.service deliberately runs in the container's
# own netns instead (the node has to see the uplinks), so joining "grout.service's
# namespace" hands FRR a fresh, empty netns: zebra then sees none of grout's
# control plane representors, if_nametoindex() returns 0 for every one of them,
# and each grout interface lands in zebra as a "pseudo interface" with ifindex 0.
# Nothing errors out -- but RAs cannot be sent, so unnumbered BGP never comes up
# and no route can be programmed. Mask that drop-in with a same-named file in
# /etc, which takes precedence, and keep FRR in the container netns with grout.
RUN mkdir -p /etc/systemd/system/frr.service.d \
 && printf '%s\n' \
    '[Service]' \
    'PrivateNetwork=false' \
    'JoinsNamespaceOf=' \
    > /etc/systemd/system/frr.service.d/grout.conf

# FRR must not start until the grout datapath has its uplinks. bgpd asks zebra
# to enable IPv6 router advertisements on each unnumbered peer interface once,
# while reading its config; if the interfaces do not exist yet zebra logs
# "IF 0 RA enable client bgp - interface unknown", no RA is ever sent and every
# unnumbered session stays Idle. The node bootstrap creates the flag file after
# programming the datapath, then starts frr.service explicitly. A missing
# condition makes systemd skip the unit (not fail it), so boot stays clean.
RUN mkdir -p /etc/systemd/system/frr.service.d \
 && printf '%s\n' \
    '[Unit]' \
    'ConditionPathExists=/run/cra/datapath-ready' \
    > /etc/systemd/system/frr.service.d/10-wait-for-datapath.conf

RUN systemctl enable grout.service grout-cra.service frr.service

# The base grout:edge image sets ENTRYPOINT to catatonit, which stays PID 1 and
# runs its argument as a child. systemd MUST be PID 1 (it needs to own the cgroup
# and reap init), so override the entrypoint to exec systemd directly.
ENTRYPOINT []
CMD ["/sbin/init"]
