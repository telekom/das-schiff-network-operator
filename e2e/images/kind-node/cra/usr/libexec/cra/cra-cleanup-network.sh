#!/bin/bash
# Cleanup network namespace and move interfaces back for CRA.
# Adapted from ansible hbn_node_deps role.

set -euo pipefail

INTERFACES_FILE="/etc/cra/interfaces"

# Move interfaces back to default namespace
if ip netns list | grep -q "^cra"; then
    if [[ -f "$INTERFACES_FILE" ]]; then
        while IFS= read -r intf_name; do
            # Skip empty lines and comments
            [[ -z "$intf_name" || "$intf_name" =~ ^[[:space:]]*# ]] && continue

            # Try to move interface back to default namespace
            if ip netns exec cra ip link show "$intf_name" &>/dev/null; then
                ip netns exec cra ip link set "$intf_name" netns 1 || {
                    echo "Warning: Failed to move $intf_name back to default namespace" >&2
                }
            fi
        done < "$INTERFACES_FILE"
    fi

    # Delete hbn veth pair
    if ip link show hbn &>/dev/null; then
        ip link delete hbn 2>/dev/null || true
    fi

    # Delete the network namespace
    ip netns delete cra || {
        echo "Warning: Failed to delete cra namespace" >&2
    }
fi

echo "CRA FRR network cleanup complete"
exit 0
