#!/usr/bin/python3
import grpc
from google.protobuf.any_pb2 import Any

import attribute_pb2
import capability_pb2
import gobgp_pb2
import gobgp_pb2_grpc
import ipaddress
import subprocess

import json

_TIMEOUT_SECONDS = 10

channel = grpc.insecure_channel('localhost:50051')
stub = gobgp_pb2_grpc.GobgpApiStub(channel)

def add_path(vrf, ip: ipaddress.IPv4Network|ipaddress.IPv6Network, as_path, nh):
    if len(as_path) == 0:
        return
    if as_path[0] != 64496:
        return
    as_path.insert(0, 64496)

    nlri = Any()
    nlri.Pack(attribute_pb2.IPAddressPrefix(
        prefix_len=ip.prefixlen,
        prefix=str(ip.network_address),
    ))
    origin = Any()
    origin.Pack(attribute_pb2.OriginAttribute(
        origin=2,
    ))
    as_segment = attribute_pb2.AsSegment(
        type=2,
        numbers=as_path,
    )
    as_path = Any()
    as_path.Pack(attribute_pb2.AsPathAttribute(
        segments=[as_segment],
    ))
    attributes = [as_path]

    next_hop = Any()
    next_hop.Pack(attribute_pb2.NextHopAttribute(
        next_hop=nh,
    ))
    attributes = [origin, as_path, next_hop]

    family = gobgp_pb2.Family.AFI_IP if type(ip) == ipaddress.IPv4Network else gobgp_pb2.Family.AFI_IP6

    stub.AddPath(
        gobgp_pb2.AddPathRequest(
            table_type=gobgp_pb2.VRF,
            vrf_id=vrf,
            path=gobgp_pb2.Path(
                nlri=nlri,
                pattrs=attributes,
                family=gobgp_pb2.Family(afi=family, safi=gobgp_pb2.Family.SAFI_UNICAST),
            )
        ),
        _TIMEOUT_SECONDS,
    )

vrfs = []

with open('ipv4.json') as f:
    ipv4 = json.load(f)

with open('ipv6.json') as f:
    ipv6 = json.load(f)

vrfs = sorted(set(list(ipv4.keys()) + list(ipv6.keys())))
vlan = 100
vrf_map = {}
for vrf in vrfs:
    if vrf == "default":
        continue
    vrf_map[vrf] = vlan
    vlan = vlan + 1

for vrf, routes in ipv4.items():
    if vrf == "default":
        continue
    vrf_id = vrf_map[vrf]
    nh = f"192.168.{vrf_id}.1"
    try:
        subprocess.check_output(["gobgp", "vrf", "add", vrf, "rd", f"{vrf_id}:{vrf_id}", "rt", "both", f"{vrf_id}:{vrf_id}"])
    except:
        pass

    for route, paths in routes["routes"].items():
        for path in paths:
            if "bestpath" not in path:
                continue
            if not path["bestpath"]:
                continue
            if path["path"] == '':
                continue
            add_path(vrf, ipaddress.ip_network(path["network"]), [int(a) for a in path["path"].strip().split(" ")], nh)

for vrf, routes in ipv6.items():
    if vrf == "default":
        continue
    vrf_id = vrf_map[vrf]
    nh = f"fd00:cafe:{vrf_id}::1"
    try:
        subprocess.check_output(["gobgp", "vrf", "add", vrf, "rd", f"{vrf_id}:{vrf_id}", "rt", "both", f"{vrf_id}:{vrf_id}"])
    except:
        pass

    for route, paths in routes["routes"].items():
        for path in paths:
            if "bestpath" not in path:
                continue
            if not path["bestpath"]:
                continue
            if path["path"] == '':
                continue
            add_path(vrf, ipaddress.ip_network(path["network"]), [int(a) for a in path["path"].strip().split(" ")], nh)

for vrf in vrfs:
    if vrf == "default":
        continue
    vlan = vrf_map[vrf]
    subprocess.check_output(["ip", "link", "add", "link", "eth1", "name", vrf, "type", "vlan", "id", str(vlan)])
    subprocess.check_output(["ip", "link", "set", "dev", vrf, "up"])
    subprocess.check_output(["ip", "address", "add", f"192.168.{vlan}.1/30", "dev", vrf])
    subprocess.check_output(["gobgp", "neighbor", "add", f"192.168.{vlan}.2", "as", "64497", "vrf", vrf])
    subprocess.check_output(["ip", "address", "add", f"fd00:cafe:{vlan}::1/126", "dev", vrf])
    subprocess.check_output(["gobgp", "neighbor", "add", f"fd00:cafe:{vlan}::2", "as", "64497", "vrf", vrf])
