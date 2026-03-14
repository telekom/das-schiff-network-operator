#!/bin/bash
# Start NAT64 gateway (tayga + unbound).
set -e

# Create tayga TUN device (idempotent)
mkdir -p /var/lib/tayga
tayga --mktun 2>/dev/null || true
ip link set nat64 up
ip addr add 10.64.0.1/16 dev nat64 2>/dev/null || true
ip -6 addr add fda5:25c1:193e::1/128 dev nat64 2>/dev/null || true
ip route add 10.64.0.0/16 dev nat64 2>/dev/null || true
ip -6 route add 64:ff9b::/96 dev nat64 2>/dev/null || true

# Enable forwarding + NAT
sysctl -w net.ipv4.ip_forward=1
sysctl -w net.ipv6.conf.all.forwarding=1
iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE 2>/dev/null || true

# Start services
unbound -c /etc/unbound/unbound.conf &
tayga -d &
wait
