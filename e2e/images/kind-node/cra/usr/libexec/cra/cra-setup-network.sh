#!/bin/bash
# Setup network namespace and veth pairs for CRA (FRR flavour).
# Adapted from ansible hbn_node_deps role.

set -euo pipefail

INTERFACES_FILE="/etc/cra/interfaces"

# Create network namespace
if ! ip netns list | grep -q "^cra"; then
    ip netns add cra
fi

# Process each interface from the interfaces file
if [[ -f "$INTERFACES_FILE" ]]; then
    while IFS= read -r intf_name; do
        # Skip empty lines and comments
        [[ -z "$intf_name" || "$intf_name" =~ ^[[:space:]]*# ]] && continue

        # Move interface to CRA namespace
        ip link set "$intf_name" netns cra || {
            echo "Warning: Failed to move $intf_name to cra namespace" >&2
        }
    done < "$INTERFACES_FILE"
fi

# Create veth pair for HBN (FRR: hbn on both sides)
if ! ip netns exec cra ip link show hbn &>/dev/null; then
    ip link add hbn type veth peer hbn netns cra
fi

echo "CRA FRR network setup complete"
exit 0
