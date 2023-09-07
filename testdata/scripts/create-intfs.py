import json
import sys
import subprocess

with open('/ipv4.json') as f:
    ipv4 = json.load(f)
with open('/ipv6.json') as f:
    ipv6 = json.load(f)

vni_map = {
    "Vrf_one": 1,
    "Vrf_two": 2,
    "Vrf_boot": 10,
    "Vrf_mgmt": 20,
    "Vrf_storage": 30,
    "Vrf_internet": 42
}

vrfs = sorted(set(list(ipv4.keys()) + list(ipv6.keys())))

subprocess.check_output(["ip", "a", "add", sys.argv[1], "dev", "lo"])

vlan = 100
for vrf in vrfs:
    if vrf == "default":
        continue
    intf_name = vrf.replace("Vrf_", "ll.")
    vni = vni_map[vrf]
    subprocess.check_output(["ip", "link", "add", "link", "eth1", "name", intf_name, "type", "vlan", "id", str(vlan)])
    subprocess.check_output(["ip", "link", "set", "dev", intf_name, "up"])
    subprocess.check_output(["ip", "link", "add", vrf, "type", "vrf", "table", str(vlan)])
    subprocess.check_output(["ip", "link", "set", "dev", vrf, "up"])
    subprocess.check_output(["ip", "link", "set", "dev", intf_name, "master", vrf])
    subprocess.check_output(["ip", "address", "add", f"192.168.{vlan}.2/30", "dev", intf_name])
    subprocess.check_output(["ip", "address", "add", f"fd00:cafe:{vlan}::2/126", "dev", intf_name])

    subprocess.check_output(["ip", "link", "add", f"vx.{vlan}", "type", "vxlan", "id", f"{vni}", "dev", "lo", "local", sys.argv[1], "dstport", "4789", "nolearning"])
    subprocess.check_output(["ip", "link", "add", f"br.{vlan}", "type", "bridge"])
    subprocess.check_output(["ip", "link", "set", f"vx.{vlan}", "master", f"br.{vlan}"])
    subprocess.check_output(["ip", "link", "set", f"br.{vlan}", "master", vrf])
    subprocess.check_output(["ip", "link", "set", f"br.{vlan}", "up"])
    subprocess.check_output(["ip", "link", "set", f"vx.{vlan}", "up"])

    vlan = vlan + 1
