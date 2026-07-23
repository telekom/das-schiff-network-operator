#!/bin/bash
# Setup network namespace for CRA (grout flavour).
#
# grout overlay: the grout branch of the production cra-setup-network.sh. Unlike
# FRR (veth `hbn` on both sides) or vSR (infra veths), grout OWNS its ports as
# DPDK ports and cannot adopt a moved-in kernel veth. So this script only:
#   1. creates netns `cra` (grout runs inside it via nerdctl --network=ns:),
#   1b. creates a dedicated agent<->sidecar management veth `cra-mgmt` (the vSR
#      "netconf" pattern, minus NETCONF): grout's `hbn` is a DATA net_tap so the
#      agent reaches the grout-cra sidecar over this /127+/31 mgmt veth instead,
#   2. moves the fabric uplinks into netns `cra` for grout to bind, and
#   3. ensures hugepages are available (DPDK mempools).
# The node<->grout data trunk (`hbn`) is a grout-created net_tap whose kernel
# netdev is moved to the HOST by cra-start.sh AFTER grout is up (a veth cannot be
# handed to grout, so the tap direction is inverted vs. FRR/vSR).

set -euo pipefail

INTERFACES_FILE="/etc/cra/interfaces"
UPLINK_MODE_FILE="/etc/cra/uplink-mode"   # "vfio" (prod PCIe) or "tap" (lab veth)
HUGEPAGES_MIN="${CRA_HUGEPAGES_MIN:-1024}" # number of 2MB hugepages to ensure

# Read the uplink mode (default to lab net_tap; prod sets "vfio").
UPLINK_MODE="tap"
if [[ -f "$UPLINK_MODE_FILE" ]]; then
    UPLINK_MODE=$(tr -d '[:space:]' < "$UPLINK_MODE_FILE")
fi

# 1. Create the CRA network namespace.
if ! ip netns list | grep -q "^cra"; then
    ip netns add cra
fi
# grout needs loopback up inside the netns for VTEP/local addresses.
ip -n cra link set lo up || true

# 1b. Dedicated agent<->sidecar MANAGEMENT veth (the vSR "netconf" pattern,
#     without NETCONF). The FRR flavour reaches its cra sidecar over the `hbn`
#     veth because there hbn is a plain kernel veth. In grout, `hbn` is a DPDK
#     net_tap DATA trunk (created by grout, moved to the HOST by cra-start.sh) and
#     carries no kernel management IP, so the hostNetwork agent CANNOT reach the
#     grout-cra sidecar over hbn. Instead a dedicated /127 + /31 veth links the
#     host (agent, root netns) to netns cra (grout-cra sidecar):
#       cra side : fd00:7:caa5:: / 169.254.1.0   (grout-cra binds :8443 here)
#       host side: fd00:7:caa5::1 / 169.254.1.1  (agent CRA_URL origin)
#     These match config/agent-cra-grout/agent.yaml CRA_URL and the grout-cra
#     `-ip` default. Assigned BEFORE cra-start.sh launches the sidecar so its
#     bind() to fd00:7:caa5:: succeeds.
if ! ip -n cra link show cra-mgmt &>/dev/null; then
    ip link add cra-mgmt type veth peer cra-mgmt netns cra
fi
# host (root netns) end
ip addr add fd00:7:caa5::1/127 dev cra-mgmt 2>/dev/null || true
ip addr add 169.254.1.1/31 dev cra-mgmt 2>/dev/null || true
ip link set cra-mgmt up || true
# cra (sidecar) end
ip -n cra addr add fd00:7:caa5::/127 dev cra-mgmt 2>/dev/null || true
ip -n cra addr add 169.254.1.0/31 dev cra-mgmt 2>/dev/null || true
ip -n cra link set cra-mgmt up || true

# 2. Move fabric uplinks into netns cra.
#    - vfio (prod): the PCIe NICs are bound to vfio-pci and are NOT kernel
#      netdevs, so there is nothing to move here; grout binds them by PCI
#      address (cra-start.sh reads /etc/cra/uplinks). prod-node-prep handles the
#      vfio-pci / IOMMU binding.
#    - tap (lab): the fabric uplinks are kernel veths; move them into netns cra
#      so grout can present a net_tap paired to each (cra-start.sh).
if [[ "$UPLINK_MODE" == "tap" && -f "$INTERFACES_FILE" ]]; then
    while IFS= read -r intf_name; do
        [[ -z "$intf_name" || "$intf_name" =~ ^[[:space:]]*# ]] && continue
        ip link set "$intf_name" netns cra || \
            echo "Warning: Failed to move $intf_name to cra namespace" >&2
        ip -n cra link set "$intf_name" up mtu 9100 || true
    done < "$INTERFACES_FILE"
fi

# 3. Ensure hugepages for DPDK. grout's net_tap/net_vhost mempools exhaust
#    without hugepages (test-mode `-t` no-huge only survives a couple of ports).
#    prod-node-prep reserves these persistently; this is a lab-friendly fallback.
if ! mountpoint -q /dev/hugepages; then
    mkdir -p /dev/hugepages
    mount -t hugetlbfs nodev /dev/hugepages || \
        echo "Warning: Failed to mount hugetlbfs at /dev/hugepages" >&2
fi
if [[ -w /proc/sys/vm/nr_hugepages ]]; then
    current=$(cat /proc/sys/vm/nr_hugepages 2>/dev/null || echo 0)
    if [[ "$current" -lt "$HUGEPAGES_MIN" ]]; then
        echo "$HUGEPAGES_MIN" > /proc/sys/vm/nr_hugepages || \
            echo "Warning: Failed to reserve $HUGEPAGES_MIN hugepages" >&2
    fi
fi

echo "CRA grout network setup complete (uplink mode: $UPLINK_MODE)"
exit 0
