#!/usr/bin/env python
import ipaddress
import random
import json
from typing import List
vni_map = {
    "default": 0,
    "Vrf_one": 1,
    "Vrf_two": 2,
    "Vrf_boot": 10,
    "Vrf_mgmt": 20,
    "Vrf_storage": 30,
    "Vrf_internet": 42
}

## Underlay Networks
underlay_network_fabric_ipv4 = "192.0.2.0/25"
underlay_network_node_ipv4 = "192.0.2.128/25"

## Overlay Networks
overlay_cluster_network_k8s_node_ipv4 = "198.51.100.0/24"
overlay_cluster_network_k8s_node_ipv6 = "2001:db8:ffff:ffff::/64"

## Networks which are used for the Route-Dicer
routes_ipv4 = "203.0.113.0/24"
routes_ipv6 =  "2001:db8::/48"

## Networks which are used for the Route-Dicer
# Route AS Numbers
# 65536-65551
vrf_routes_as_numbers = range(65536,65552)

py_network_ipv4 = ipaddress.ip_network(routes_ipv4)
routes_ipv4 = list(py_network_ipv4.subnets(new_prefix=32))
routes_ipv4_len = len(routes_ipv4)
py_network_ipv6 = ipaddress.ip_network(routes_ipv6)
routes_ipv6 = list(py_network_ipv6.subnets(new_prefix=64))
routes_ipv6_len = len(routes_ipv6)

vni_map_len = len(vni_map)
slice_ipv4_len = int(routes_ipv4_len / vni_map_len)
slice_ipv6_len = int(routes_ipv6_len / vni_map_len)

vrf_routes_ipv4 = {}
vrf_routes_ipv6 = {}



def gen_as_path() -> List[str]:
    return [str(integer) for integer in random.sample(vrf_routes_as_numbers, random.randint(1, len(vrf_routes_as_numbers)))]

for vrf in vni_map.keys():
    vrf_index = list(vni_map).index(vrf)
    start_ipv4 = vrf_index * slice_ipv4_len
    end_ipv4 = (vrf_index + 1) * slice_ipv4_len

    start_ipv6 = vrf_index * slice_ipv6_len
    end_ipv6 = (vrf_index + 1) * slice_ipv6_len 

    vrf_routes_ipv4[vrf] = {
        "routes": {
            str(route): [
                {
                    "valid": True,
                    "bestpath": True,
                    "pathFrom": "external",
                    "prefix": str(route.network_address),
                    "prefixLen": route.prefixlen,
                    "network": str(route),
                    "metric":45,
                    "weight":0,
                    "path": " ".join(gen_as_path()),
                    "origin":"IGP",
                    "nexthops":[
                    {
                        "ip": "0.0.0.0",
                        "afi": "ipv4",
                        "used": True
                    }
                    ]
                },
            ]
            for route in routes_ipv4[start_ipv4:end_ipv4]
        }
    }
    vrf_routes_ipv6[vrf] = {
        "routes": {
            str(route): [
                {
                    "valid": True,
                    "bestpath": True,
                    "pathFrom": "external",
                    "prefix": str(route.network_address),
                    "prefixLen": route.prefixlen,
                    "network": str(route),
                    "metric":45,
                    "weight":0,
                    "path": " ".join(gen_as_path()),
                    "origin":"IGP",
                    "nexthops":[
                    {
                        "ip": "0.0.0.0",
                        "afi": "ipv4",
                        "used": True
                    }
                    ]
                },
            ]
            for route in routes_ipv6[start_ipv6:end_ipv6]
        }
    }

with open("ipv4.json", "w") as file:
    json.dump(vrf_routes_ipv4, file, indent=2)
with open("ipv6.json", "w") as file:
    json.dump(vrf_routes_ipv6, file, indent=2)

