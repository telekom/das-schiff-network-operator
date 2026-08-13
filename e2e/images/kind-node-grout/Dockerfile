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

# ── kube-vip static pod manifest template (VIP injected at setup time) ──
# Same as the FRR node image: the harness renders the template into
# /etc/kubernetes/manifests before kubeadm init, so the directory and template
# have to exist here too.
RUN mkdir -p /etc/kube-vip
COPY ${DSN}/kube-vip.yaml /etc/kube-vip/kube-vip.yaml.tpl

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
