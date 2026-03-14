# CRA base configuration template.
# Per-node values are injected by e2e/scripts/configure-underlay.sh.
# Matches vm-lab: per-interface BGP unnumbered, hbn veth trunk.
vtepLoopbackIP: "{{ .VtepIP }}"
exportCIDRs:
  - "10.100.0.0/24"
  - "fdcb:f93c:3a3e::/64"
localASN: 64497
trunkInterfaceName: "hbn"
underlayNeighbors:
  - interface: eth1
    remoteASN: "65500"
    localASN: "65501"
    keepaliveTime: 30
    holdTime: 90
    bfdMinTimer: 333
    ipv4: true
    evpn: false
  - interface: eth2
    remoteASN: "65500"
    localASN: "65501"
    keepaliveTime: 30
    holdTime: 90
    bfdMinTimer: 333
    ipv4: true
    evpn: false
  - ip: "192.0.2.1"
    remoteASN: "64497"
    keepaliveTime: 30
    holdTime: 90
    bfdMinTimer: 333
    ipv4: false
    evpn: true
  - ip: "192.0.2.2"
    remoteASN: "64497"
    keepaliveTime: 30
    holdTime: 90
    bfdMinTimer: 333
    ipv4: false
    evpn: true
clusterNeighbors:
  - ip: "{{ .NodeIPv4 }}"
    updateSource: "169.254.100.100"
    remoteASN: "65170"
    localASN: "65169"
    keepaliveTime: 30
    holdTime: 90
    bfdMinTimer: 333
    ipv4: true
  - ip: "{{ .NodeIPv6 }}"
    updateSource: "fd00:7:caa5:1::"
    remoteASN: "65170"
    localASN: "65169"
    keepaliveTime: 30
    holdTime: 90
    bfdMinTimer: 333
    ipv6: true
  - ip: "fd00:7:caa5::1"
    remoteASN: "65170"
    localASN: "65169"
    keepaliveTime: 30
    holdTime: 90
    bfdMinTimer: 333
    ipv6: true
    ipv4: true
managementVRF:
  name: mgmt
  vni: 20
  evpnRouteTarget: "64497:20"
clusterVRF:
  name: cluster
  vni: 30
  evpnRouteTarget: "64497:30"
