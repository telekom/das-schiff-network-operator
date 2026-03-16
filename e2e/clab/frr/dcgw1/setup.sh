#!/bin/bash
set -euo pipefail
# Post-deploy setup for dcgw1 (matches vm-lab dcgw1 exec block)

sysctl -w net.ipv4.ip_forward=1
sysctl -w net.ipv6.conf.all.forwarding=1

# VRF mgmt
ip link add vr.mgmt type vrf table 100
ip link add vx.mgmt type vxlan id 20 dev lo local 192.0.2.1 dstport 4789 nolearning
ip link add br.mgmt type bridge
ip link set vx.mgmt master br.mgmt
ip link set br.mgmt master vr.mgmt
ip link set br.mgmt up
ip link set vx.mgmt up
ip link set vr.mgmt up

# Tester interface in mgmt VRF
ip link set eth-tester master vr.mgmt
ip addr add 10.200.0.1/24 dev eth-tester
ip addr add fd00:200::1/64 dev eth-tester
ip link set eth-tester up

# NAT64 gateway
ip link set eth-nat64 master vr.mgmt
ip addr add fddf:64ff:9b00:1::/127 dev eth-nat64
ip link set eth-nat64 up
# Static routes to NAT64 in vr.mgmt
ip -6 route add fda5:25c1:193e::1/128 via fddf:64ff:9b00:1::1 dev eth-nat64 vrf vr.mgmt
ip -6 route add 64:ff9b::/96 via fddf:64ff:9b00:1::1 dev eth-nat64 vrf vr.mgmt

echo "dcgw1 setup complete"
