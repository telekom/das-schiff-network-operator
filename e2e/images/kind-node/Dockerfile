# Custom kind node image for das-schiff-network-operator e2e tests.
# Extends kindest/node with CRA host-side tooling and the CRA-FRR machine
# image baked in at build time.
ARG KIND_NODE_VERSION=v1.35.1
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
      bpftool \
      tcpdump \
      curl \
      ca-certificates \
      netplan.io && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# ── Reference CNI plugins ──
# Baked in at image build time rather than downloaded during lab bring-up: the
# kind nodes have no reliable egress to github.com in CI, and kindest/node only
# ships the handful of plugins kindnet needs (host-local, loopback, portmap,
# ptp). Without this, the routed KubeVirt fixture's static IPAM fails CNI ADD
# with `failed to find plugin "static" in path [/opt/cni/bin]`, which the
# kubelet retries forever, so the VMI never leaves Scheduling and the lab hangs
# until the job times out instead of failing fast.
# The kubelet loads plugins from /opt/cni/bin. macvlan is additionally copied to
# /usr/lib/cni (the legacy Debian path) to preserve the behaviour the previous
# bring-up-time install intended; the base image does not create that directory,
# so the old `cp` silently failed.
ARG CNI_PLUGINS_VERSION=v1.9.0
ARG CNI_PLUGINS_SHA256_amd64=58c03705426e929658f45a851df15a86d06ef680cacbf3f2dc127731ca265c28
ARG CNI_PLUGINS_SHA256_arm64=2596ef56329dd1269026f46b8df262f09ba43c92dbfb940e1e69fbccccd30a29
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) sha="${CNI_PLUGINS_SHA256_amd64}" ;; \
      arm64) sha="${CNI_PLUGINS_SHA256_arm64}" ;; \
      *) echo "unsupported architecture: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /tmp/cni-plugins.tgz \
      "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-${arch}-${CNI_PLUGINS_VERSION}.tgz"; \
    echo "${sha}  /tmp/cni-plugins.tgz" | sha256sum -c -; \
    mkdir -p /opt/cni/bin /usr/lib/cni; \
    tar -xzf /tmp/cni-plugins.tgz -C /opt/cni/bin; \
    rm -f /tmp/cni-plugins.tgz /opt/cni/bin/LICENSE /opt/cni/bin/README.md; \
    cp /opt/cni/bin/macvlan /usr/lib/cni/macvlan; \
    test -x /opt/cni/bin/static

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
