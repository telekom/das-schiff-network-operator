#!/bin/bash
set -euo pipefail
# Post-deploy setup for dcgw2 (matches vm-lab dcgw2 exec block)

sysctl -w net.ipv4.ip_forward=1
sysctl -w net.ipv6.conf.all.forwarding=1

ip addr add 192.0.2.2/32 dev lo

# VRF mgmt
ip link add vr.mgmt type vrf table 100
ip link add vx.mgmt type vxlan id 20 dev lo local 192.0.2.2 dstport 4789 nolearning
ip link add br.mgmt type bridge
ip link set vx.mgmt master br.mgmt
ip link set br.mgmt master vr.mgmt
ip link set br.mgmt up
ip link set vx.mgmt up
ip link set vr.mgmt up

# VRF m2m
ip link add vr.m2m type vrf table 2002026
ip link add vx.m2m type vxlan id 2002026 dev lo local 192.0.2.2 dstport 4789 nolearning
ip link add br.m2m type bridge
ip link set vx.m2m master br.m2m
ip link set br.m2m master vr.m2m
ip link set m2mgw master vr.m2m
ip link set br.m2m up
ip link set vx.m2m up
ip link set vr.m2m up
ip addr add fdff:965f:f02a:2::/127 dev m2mgw

# VRF c2m
ip link add vr.c2m type vrf table 2002027
ip link add vx.c2m type vxlan id 2002027 dev lo local 192.0.2.2 dstport 4789 nolearning
ip link add br.c2m type bridge
ip link set vx.c2m master br.c2m
ip link set br.c2m master vr.c2m
ip link set c2mgw master vr.c2m
ip link set br.c2m up
ip link set vx.c2m up
ip link set vr.c2m up
ip addr add fde9:9ec5:829e:2::/127 dev c2mgw

# Static routes to m2mgw/c2mgw
ip -4 route add 10.102.0.0/24 via inet6 fdff:965f:f02a:2::1 dev m2mgw vrf vr.m2m
ip -6 route add fda5:25c1:193c::/64 via inet6 fdff:965f:f02a:2::1 dev m2mgw vrf vr.m2m
ip -4 route add 10.102.1.0/24 via inet6 fde9:9ec5:829e:2::1 dev c2mgw vrf vr.c2m
ip -6 route add fda5:25c1:193d::/64 via inet6 fde9:9ec5:829e:2::1 dev c2mgw vrf vr.c2m

# NAT64 gateway
ip link set eth-nat64 master vr.mgmt
ip addr add fddf:64ff:9b00:2::/127 dev eth-nat64
ip link set eth-nat64 up
# Static routes to NAT64 in vr.mgmt
ip -6 route add fda5:25c1:193e::1/128 via fddf:64ff:9b00:2::1 dev eth-nat64 vrf vr.mgmt
ip -6 route add 64:ff9b::/96 via fddf:64ff:9b00:2::1 dev eth-nat64 vrf vr.mgmt

echo "dcgw2 setup complete"
