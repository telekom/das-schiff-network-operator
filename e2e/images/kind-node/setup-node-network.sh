#!/bin/bash
# Add node overlay IPs to loopback before kubelet starts.
# Reads /etc/node-identity.env (mounted per-node via Kind extraMounts).
# This ensures kubelet's --node-ip is valid at first boot.
set -e

ENV_FILE=/etc/node-identity.env

if [ ! -f "$ENV_FILE" ]; then
  echo "setup-node-network: $ENV_FILE not found — skipping"
  exit 0
fi

# shellcheck source=/dev/null
. "$ENV_FILE"

ip addr add "${NODE_IPV4}/32" dev lo 2>/dev/null || true
ip addr add "${NODE_IPV6}/128" dev lo 2>/dev/null || true

echo "setup-node-network: loopback IPs added: $NODE_IPV4 $NODE_IPV6"
