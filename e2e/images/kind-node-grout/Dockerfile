# Self-contained grout kind-node image for cra-grout e2e.
#
# FROM kindest/node + das-schiff e2e host tooling + the grout CRA tooling
# (nerdctl runtime, like the vSR overlay). The real das-schiff-cra-grout image
# is saved by the e2e harness/Makefile before this node image is built and then
# imported into the node's containerd namespace at build time.
#
# Build context = das-schiff-network-operator repo root:
#   docker build -f e2e/images/kind-node-grout/Dockerfile -t kind-node-grout .
ARG KIND_NODE_VERSION=v1.35.1
FROM kindest/node:${KIND_NODE_VERSION}

ARG NERDCTL_VERSION=2.1.2
ARG DSN=e2e/images/kind-node

# ── Host-side deps (apt; works in CI with mirrors) ──
RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      skopeo umoci systemd-container iputils-ping mtr-tiny iproute2 \
      bridge-utils bpftool tcpdump curl netplan.io && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# Disable udevadm (not functional in containers; netplan apply calls
# `udevadm control --reload`, which fails when systemd-udevd isn't running).
RUN rm -f /usr/bin/udevadm && ln -s /usr/bin/true /usr/bin/udevadm

# ── das-schiff kind-node host tooling (everything EXCEPT its FRR cra/) ──
COPY ${DSN}/10-cra.conf /etc/sysctl.d/10-cra.conf
RUN mkdir -p /etc/systemd/networkd.conf.d
COPY ${DSN}/networkd.conf /etc/systemd/networkd.conf.d/cra.conf
COPY ${DSN}/10-eth0-strip.network /etc/systemd/network/10-eth0-strip.network
RUN systemctl enable systemd-networkd
COPY ${DSN}/fix-vrf-rules.service /lib/systemd/system/
RUN systemctl enable fix-vrf-rules.service
COPY ${DSN}/setup-node-network.service /lib/systemd/system/
RUN systemctl enable setup-node-network.service

# ── nerdctl (drives the node's own containerd, namespace hbr) ──
RUN arch="$(dpkg --print-architecture)"; \
    curl -fsSL -o /tmp/nerdctl.tgz \
      "https://github.com/containerd/nerdctl/releases/download/v${NERDCTL_VERSION}/nerdctl-${NERDCTL_VERSION}-linux-${arch}.tar.gz" && \
    tar -C /usr/local/bin -xzf /tmp/nerdctl.tgz nerdctl && rm /tmp/nerdctl.tgz

# ── grout CRA tooling (overrides the FRR cra-generator with the nerdctl one) ──
COPY e2e/images/kind-node-grout/cra/ /
RUN chmod +x \
      /usr/lib/systemd/system-generators/cra-generator \
      /usr/libexec/cra/*.sh && \
    mkdir -p /etc/cra && echo grout > /etc/cra/flavour

# ── real CRA grout image (saved by e2e setup before this build) ──
COPY e2e/images/kind-node-grout/cra-grout.tar /tmp/cra-grout.tar
RUN containerd & \
    sleep 5 && \
    ctr -n=hbr images import --no-unpack /tmp/cra-grout.tar && \
    echo das-schiff-cra-grout:latest > /etc/cra/image && \
    rm -f /tmp/cra-grout.tar

LABEL description="das-schiff kind-node + grout CRA (nerdctl), self-contained for cra-grout e2e"
