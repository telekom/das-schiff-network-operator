#!/usr/bin/env bash
echo "${KUBECONFIG}"

ip netns add test
ip netns list

## install frr via the package from aur or the package manager
## configure pam for wheel/sudo users
## /etc/pam.d/frr                                                                                                                                            ✔  21s  
# #%PAM-1.0
# #

# ##### if running frr as root:
# # Only allow root (and possibly wheel) to use this because enable access
# # is unrestricted.
# auth       sufficient   pam_rootok.so
# account    sufficient   pam_rootok.so

# # Uncomment the following line to implicitly trust users in the "wheel" group.
# auth       sufficient   pam_wheel.so trust use_uid
# # Uncomment the following line to require a user to be in the "wheel" group.
# auth       required     pam_wheel.so use_uid
# ###########################################################

# # If using frr privileges and with a seperate group for vty access, then
# # access can be controlled via the vty access group, and pam can simply
# # check for valid user/password, eg:
# #
# # only allow local users.
# #auth       required     pam_securetty.so
# #auth       include      system-auth
# #auth       required     pam_nologin.so
# #account    include      system-auth
# #password   include      system-auth
# #session    include      system-auth
# #session    optional     pam_console.so

## for this to work you need a special systemd-unit patch for frr
# # /etc/systemd/system/frr.service.d/override.conf
# [Service]
# NetworkNamespacePath=/var/run/netns/test
 
systemctl start frr
systemctl status frr

ip netns exec test sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec test sysctl -w net.ipv4.ip_forward=1

ip netns exec test sysctl -w net.ipv4.fib_multipath_hash_policy=1
ip netns exec test sysctl -w net.ipv6.fib_multipath_hash_policy=1

ip netns exec test ip -4 rule add pref 32765 table local
ip netns exec test ip -4 rule del pref 0
ip netns exec test ip -6 rule add pref 32765 table local
ip netns exec test ip -6 rule del pref 0

ip netns exec test sysctl -w net.ipv6.conf.all.autoconf=1
ip netns exec test sysctl -w net.ipv6.conf.all.accept_ra=1

ip netns exec test ip link set dev lo up

ip netns exec test ip link add Vrf_underlay type vrf table 2
ip netns exec test ip link add Vrf_mgmt type vrf table 3
ip netns exec test ip link add Vrf_coil type vrf table 119
ip netns exec test ip link add Vrf_nwop type vrf table 130
ip netns exec test ip link add Vrf_kubevip type vrf table 198


ip netns exec test ip link add dum.underlay type dummy
ip netns exec test ip link add name br.mgmt type bridge
ip netns exec test ip link add name br.cluster type bridge

ip netns exec test ip link set dev dum.underlay master Vrf_underlay
ip netns exec test ip link set dev br.mgmt master Vrf_mgmt
ip netns exec test ip link set dev Vrf_underlay up
ip netns exec test ip link set dev Vrf_mgmt up
ip netns exec test ip link set dev Vrf_coil up
ip netns exec test ip link set dev Vrf_nwop up
ip netns exec test ip link set dev Vrf_kubevip up

ip netns exec test ip addr add 192.0.2.128/32 dev dum.underlay
ip netns exec test ip link set dev dum.underlay up

ip netns exec test ip link add vx.20 type vxlan id 20 local 192.0.2.128 nolearning dev dum.underlay dstport 4789
ip netns exec test ip link add vx.1000 type vxlan id 1000 local 192.0.2.128 nolearning dev dum.underlay dstport 4789
ip netns exec test ip link set dev vx.20 master br.mgmt
ip netns exec test ip link set dev vx.1000 master br.cluster
ip netns exec test ip link set dev vx.20 mtu 1500
ip netns exec test ip link set dev vx.1000 mtu 9000
ip netns exec test ip link set dev vx.20 up
ip netns exec test ip link set dev vx.1000 up
ip netns exec test ip addr add 198.51.100.0/32 dev br.cluster
ip netns exec test ip link set dev br.mgmt mtu 1500
ip netns exec test ip link set dev br.cluster mtu 9000
ip netns exec test ip link set dev br.mgmt up
ip netns exec test ip link set dev br.cluster up

ip netns exec test ip link add def_mgmt type veth peer name mgmt_def

## This part connects the test netns with our 
## internal containerlab setup
ip link add cp1 type veth peer name ens1f0
ip link add cp2 type veth peer name ens1f1
ip link add cp3 type veth peer name ens2f0
ip link add cp4 type veth peer name ens2f1

ip link set ens1f0 netns test
ip link set ens1f1 netns test
ip link set ens2f0 netns test
ip link set ens2f1 netns test

ip link set dev cp1 master srv5-p1
ip link set dev cp2 master srv5-p2
ip link set dev cp3 master srv4-p1
ip link set dev cp4 master srv4-p2

ip link set dev cp1 mtu 9100
ip link set dev cp2 mtu 9100
ip link set dev cp3 mtu 9100
ip link set dev cp4 mtu 9100

ip link set dev cp1 up
ip link set dev cp2 up
ip link set dev cp3 up
ip link set dev cp4 up

ip netns exec test ip link set dev ens1f0 master Vrf_underlay
ip netns exec test ip link set dev ens1f1 master Vrf_underlay
ip netns exec test ip link set dev ens2f0 master Vrf_underlay
ip netns exec test ip link set dev ens2f1 master Vrf_underlay

ip netns exec test ip link set dev ens1f0 mtu 9100
ip netns exec test ip link set dev ens1f1 mtu 9100
ip netns exec test ip link set dev ens2f0 mtu 9100
ip netns exec test ip link set dev ens2f1 mtu 9100


ip netns exec test ip link set dev ens1f0 up
ip netns exec test ip link set dev ens1f1 up
ip netns exec test ip link set dev ens2f0 up
ip netns exec test ip link set dev ens2f1 up

ip netns exec test ip link set dev mgmt_def master Vrf_mgmt
ip netns exec test ip link set dev def_mgmt up
ip netns exec test ip link set dev mgmt_def up


CLUSTER_ENDPOINT=$(kubectl config view -o jsonpath='{.clusters[].cluster.server}')
ENDPOINT=${CLUSTER_ENDPOINT#"https://"}
ENDPOINT_PORT=${ENDPOINT##*:}

echo "${CLUSTER_ENDPOINT} :: ${ENDPOINT} ${ENDPOINT_PORT}"
# This forwards the kubernetes api into the netns test and the metrics ports into the host system
socat "unix-listen:/tmp/kube-api",fork "tcp-connect:${ENDPOINT}" & ip netns exec test socat "tcp-listen:${ENDPOINT_PORT}",fork,reuseaddr "unix-connect:/tmp/kube-api" & socat tcp-listen:7080,fork,reuseaddr exec:'ip netns exec test socat STDIO "tcp-connect:127.0.0.1:7080"',nofork && fg
