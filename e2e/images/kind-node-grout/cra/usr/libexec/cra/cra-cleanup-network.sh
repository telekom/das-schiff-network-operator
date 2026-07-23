#!/bin/bash
# Cleanup netns and move interfaces back (grout flavour).
#
# grout owns its ports as DPDK ports, so on stop the grout net_taps (hbn, per-pod
# taps, per-uplink lab taps) are destroyed with the container. This script moves
# the fabric uplinks back to the default namespace, removes the host-side `hbn`
# trunk netdev (a leftover grout tap), tears down any lab uplink bridges, and
# deletes netns cra.
set -euo pipefail

INTERFACES_FILE="/etc/cra/interfaces"
UPLINK_MODE_FILE="/etc/cra/uplink-mode"

UPLINK_MODE="tap"
[[ -f "$UPLINK_MODE_FILE" ]] && UPLINK_MODE=$(tr -d '[:space:]' < "$UPLINK_MODE_FILE")

# Remove the host-side hbn trunk (grout net_tap moved to host by cra-start.sh).
if ip link show hbn &>/dev/null; then
    ip link delete hbn 2>/dev/null || true
fi

# Remove the host-side management veth (its cra-side peer dies with netns cra,
# but delete explicitly in case the netns was already gone).
if ip link show cra-mgmt &>/dev/null; then
    ip link delete cra-mgmt 2>/dev/null || true
fi

if ip netns list | grep -q "^cra"; then
    # Move fabric uplinks (lab veths) back to the default namespace.
    if [[ "$UPLINK_MODE" == "tap" && -f "$INTERFACES_FILE" ]]; then
        while IFS= read -r intf_name; do
            [[ -z "$intf_name" || "$intf_name" =~ ^[[:space:]]*# ]] && continue
            if ip netns exec cra ip link show "$intf_name" &>/dev/null; then
                ip netns exec cra ip link set "$intf_name" netns 1 || \
                    echo "Warning: Failed to move $intf_name back" >&2
            fi
        done < "$INTERFACES_FILE"
    fi
    # Delete any lab uplink bridges left inside netns cra.
    for br in $(ip -n cra -o link show type bridge 2>/dev/null | awk -F': ' '{print $2}' | grep '^bru' || true); do
        ip -n cra link delete "$br" 2>/dev/null || true
    done
    ip netns delete cra || echo "Warning: Failed to delete cra namespace" >&2
fi

rm -rf /run/cra
echo "CRA grout network cleanup complete"
exit 0
