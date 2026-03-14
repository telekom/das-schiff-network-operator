#!/bin/bash
# Post-deploy setup for leaf1
set -e

sysctl -w net.ipv4.ip_forward=1
sysctl -w net.ipv6.conf.all.forwarding=1
ip addr add 192.0.2.10/32 dev lo

echo "leaf1 setup complete"
