#!/bin/sh
# install-cni.sh installs the cni-routed plugin binary and its CNI conflist onto
# the node, then blocks so the DaemonSet pod stays Running.
#
# Environment:
#   CNI_BIN_DIR   host path (mounted) for CNI binaries  (default /host/opt/cni/bin)
#   CNI_CONF_DIR  host path (mounted) for CNI conflists (default /host/etc/cni/net.d)
set -eu

CNI_BIN_DIR="${CNI_BIN_DIR:-/host/opt/cni/bin}"
CNI_CONF_DIR="${CNI_CONF_DIR:-/host/etc/cni/net.d}"

echo "installing cni-routed into ${CNI_BIN_DIR}"
mkdir -p "${CNI_BIN_DIR}"
cp -f /usr/local/bin/cni-routed "${CNI_BIN_DIR}/cni-routed"
chmod 0755 "${CNI_BIN_DIR}/cni-routed"

# The NetworkAttachmentDefinition carries the per-network CNI config for Multus
# secondary networks, so no standalone conflist is required here. The block
# below is left as a hook for a default/primary install if ever needed.
if [ -n "${CNI_CONF_TEMPLATE:-}" ]; then
	echo "writing CNI conf to ${CNI_CONF_DIR}/10-cni-routed.conflist"
	mkdir -p "${CNI_CONF_DIR}"
	printf '%s' "${CNI_CONF_TEMPLATE}" > "${CNI_CONF_DIR}/10-cni-routed.conflist"
fi

echo "cni-routed installed; sleeping"
# Keep the container alive (DaemonSet). Re-copy on restart handles upgrades.
while true; do sleep 3600; done
