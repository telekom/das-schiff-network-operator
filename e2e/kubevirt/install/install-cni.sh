#!/bin/sh
# install-cni.sh installs the cni-workload plugin binary and its CNI conflist onto
# the node, then blocks so the DaemonSet pod stays Running.
#
# Environment:
#   CNI_BIN_DIR        host path (mounted) for CNI binaries  (default /host/opt/cni/bin)
#   CNI_CONF_DIR       host path (mounted) for CNI conflists (default /host/etc/cni/net.d)
#   CNI_NODE_CONF_DIR  host path (mounted) for the plugin's node config
#                      (default /host/etc/cni/cni-workload)
#   CNI_NODE_CONF_SRC  node config to install, e.g. a mounted ConfigMap key
#                      (default /etc/cni-workload/config.json; skipped if absent)
set -eu

CNI_BIN_DIR="${CNI_BIN_DIR:-/host/opt/cni/bin}"
CNI_CONF_DIR="${CNI_CONF_DIR:-/host/etc/cni/net.d}"
CNI_NODE_CONF_DIR="${CNI_NODE_CONF_DIR:-/host/etc/cni/cni-workload}"
CNI_NODE_CONF_SRC="${CNI_NODE_CONF_SRC:-/etc/cni-workload/config.json}"

# The binary is staged next to its destination and renamed into place: the
# kubelet may exec the plugin at any moment (pod sandbox setup during a
# DaemonSet rollout), and an in-place cp would let it run a truncated file.
echo "installing cni-workload into ${CNI_BIN_DIR}"
mkdir -p "${CNI_BIN_DIR}"
cp -f /usr/local/bin/cni-workload "${CNI_BIN_DIR}/.cni-workload.tmp"
chmod 0755 "${CNI_BIN_DIR}/.cni-workload.tmp"
mv -f "${CNI_BIN_DIR}/.cni-workload.tmp" "${CNI_BIN_DIR}/cni-workload"

# The node config carries the plugin's trust anchors (agent socket, CRA netns,
# trunk interface). The plugin refuses them in the NetworkAttachmentDefinition
# (tenant-writable) and only reads this root-owned, non-writable file. Written
# via a temp file + rename so a plugin invocation never sees a partial file.
if [ -f "${CNI_NODE_CONF_SRC}" ]; then
	echo "installing node config into ${CNI_NODE_CONF_DIR}/config.json"
	mkdir -p "${CNI_NODE_CONF_DIR}"
	cp -f "${CNI_NODE_CONF_SRC}" "${CNI_NODE_CONF_DIR}/.config.json.tmp"
	chmod 0644 "${CNI_NODE_CONF_DIR}/.config.json.tmp"
	mv -f "${CNI_NODE_CONF_DIR}/.config.json.tmp" "${CNI_NODE_CONF_DIR}/config.json"
fi

# The NetworkAttachmentDefinition carries the per-network CNI config for Multus
# secondary networks, so no standalone conflist is required here. The block
# below is left as a hook for a default/primary install if ever needed.
if [ -n "${CNI_CONF_TEMPLATE:-}" ]; then
	echo "writing CNI conf to ${CNI_CONF_DIR}/10-cni-workload.conflist"
	mkdir -p "${CNI_CONF_DIR}"
	printf '%s' "${CNI_CONF_TEMPLATE}" > "${CNI_CONF_DIR}/10-cni-workload.conflist"
fi

echo "cni-workload installed; sleeping"
# Keep the container alive (DaemonSet). Re-copy on restart handles upgrades.
while true; do sleep 3600; done
