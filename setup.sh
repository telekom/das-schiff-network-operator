#!/usr/bin/env bash

# Create netns 
ip netns add server
ip netns add lf1
ip netns add lf2
ip netns add sp1
ip netns add sp2
ip netns add bl1
ip netns add bl2
ip netns list

# Enable v4/v6 forwarding
ip netns exec lf1 sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec lf1 sysctl -w net.ipv4.ip_forward=1
ip netns exec lf2 sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec lf2 sysctl -w net.ipv4.ip_forward=1
ip netns exec sp1 sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec sp1 sysctl -w net.ipv4.ip_forward=1
ip netns exec sp2 sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec sp2 sysctl -w net.ipv4.ip_forward=1
ip netns exec bl1 sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec bl1 sysctl -w net.ipv4.ip_forward=1
ip netns exec server sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec server sysctl -w net.ipv4.ip_forward=1

# Enable correct policy for ecmp
ip netns exec leaf sysctl -w net.ipv4.fib_multipath_hash_policy=1
ip netns exec leaf sysctl -w net.ipv6.fib_multipath_hash_policy=1
ip netns exec server sysctl -w net.ipv4.fib_multipath_hash_policy=1
ip netns exec server sysctl -w net.ipv6.fib_multipath_hash_policy=1

# IP Rule fix for server
ip netns exec server ip -4 rule add pref 32765 table local
ip netns exec server ip -4 rule del pref 0
ip netns exec server ip -6 rule add pref 32765 table local
ip netns exec server ip -6 rule del pref 0

# Enable RA support
ip netns exec server sysctl -w net.ipv6.conf.all.autoconf=1
ip netns exec leaf sysctl -w net.ipv6.conf.all.autoconf=1
ip netns exec server sysctl -w net.ipv6.conf.all.accept_ra=1
ip netns exec leaf sysctl -w net.ipv6.conf.all.accept_ra=1

# create underlay Interface with VRF
ip netns exec server ip link add Vrf_underlay type vrf table 2
ip netns exec server ip link add dum.underlay type dummy
ip netns exec server ip link set dev dum.underlay master Vrf_underlay
ip netns exec server ip link set dev Vrf_underlay up
ip netns exec server ip link set dev dum.underlay up

# Connect server with leaf
ip netns exec leaf ip link add leaf_1 type veth peer name server_1
ip netns exec leaf ip link set leaf_1 netns server
ip netns exec leaf ip link add leaf_2 type veth peer name server_2
ip netns exec leaf ip link set leaf_2 netns server
ip netns exec server ip link set leaf_1 up
ip netns exec server ip link set leaf_2 up
ip netns exec leaf ip link set server_1 up
ip netns exec leaf ip link set server_2 up


# Put leaf interfaces into Vrf_underlay
ip netns exec server ip link set dev leaf_1 master Vrf_underlay
ip netns exec server ip link set dev leaf_2 master Vrf_underlay


ip netns exec server ip addr add fc::/64 dev dum.underlay
ip netns exec server ip addr add 10.0.0.0/32 dev dum.underlay


ip netns exec leaf ip addr add fd::/64 dev lo
ip netns exec leaf ip addr add 10.0.0.1/32 dev lo


# Enable Loopback interfaces
ip netns exec server ip link set dev lo up
ip netns exec leaf ip link set dev lo up



# Show configuration
echo "################################"
echo "######      leaf-1       #######"
echo "################################"
ip netns exec leaf ip a
ip netns exec leaf ip r

echo "################################"
echo "######      leaf-2       #######"
echo "################################"
ip netns exec leaf ip a
ip netns exec leaf ip r

echo "################################"
echo "######      server       #######"
echo "################################"
ip netns exec server ip a
ip netns exec server ip r