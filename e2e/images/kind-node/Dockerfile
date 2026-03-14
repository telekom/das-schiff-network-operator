# Custom kind node image for das-schiff-network-operator e2e tests.
# Extends kindest/node with CRA host-side tooling and the CRA-FRR machine
# image baked in at build time.
ARG KIND_NODE_VERSION=v1.32.2
FROM kindest/node:${KIND_NODE_VERSION}

# ── Host-side CRA dependencies ──
RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      skopeo \
      umoci \
      systemd-container \
      iputils-ping \
      mtr-tiny \
      iproute2 \
      bridge-utils \
      tcpdump && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# sysctl tweaks (host side — CRA container has its own)
COPY 10-cra.conf /etc/sysctl.d/10-cra.conf

# systemd-networkd config to manage foreign routes/rules correctly
RUN mkdir -p /etc/systemd/networkd.conf.d
COPY networkd.conf /etc/systemd/networkd.conf.d/cra.conf

# eth0 strip config — prevent systemd-networkd from managing eth0
COPY 10-eth0-strip.network /etc/systemd/network/10-eth0-strip.network

# Enable systemd-networkd (manages hbn veth on host side)
RUN systemctl enable systemd-networkd

# CRA host-side scripts (same as ansible hbn_node_deps role deploys)
COPY cra/ /
# This copies:
#   /usr/lib/systemd/system-generators/cra-generator
#   /usr/libexec/cra/cra-setup-network.sh
#   /usr/libexec/cra/cra-cleanup-network.sh
#   /usr/libexec/cra/cra-stop.sh
#   /usr/libexec/cra/load-cra-image.sh
#   /usr/local/bin/cra-bash
#   /usr/local/bin/cra-cli

# VRF ip-rule fix service (host side)
COPY fix-vrf-rules.service /lib/systemd/system/
RUN systemctl enable fix-vrf-rules.service

# Setup-node-network: adds loopback IPs from /etc/node-identity.env before kubelet.
# /etc/node-identity.env is per-node, mounted via Kind extraMounts.
COPY setup-node-network.sh /usr/libexec/setup-node-network.sh
RUN chmod +x /usr/libexec/setup-node-network.sh
COPY setup-node-network.service /lib/systemd/system/
RUN systemctl enable setup-node-network.service

# Disable udevadm (not functional in containers)
RUN rm -f /usr/bin/udevadm && ln -s /usr/bin/true /usr/bin/udevadm

# Kubelet→CRA dependency is added later by configure-underlay.sh
# (cannot require cra.service at build time — CRA interfaces file
# does not exist until after containerlab attaches the veth pairs)
RUN mkdir -p /etc/systemd/system/kubelet.service.d

# ── CRA-FRR OCI image (baked at build time) ──
COPY cra-frr.tar /tmp/cra-frr.tar
RUN /usr/libexec/cra/load-cra-image.sh frr \
      docker-archive:/tmp/cra-frr.tar:das-schiff-cra-frr:latest && \
    rm /tmp/cra-frr.tar

# ── kube-vip static pod manifest template (VIP injected at setup time) ──
RUN mkdir -p /etc/kube-vip
COPY kube-vip.yaml /etc/kube-vip/kube-vip.yaml.tpl

LABEL description="Kind node with CRA host tooling + CRA-FRR image for das-schiff-network-operator"
