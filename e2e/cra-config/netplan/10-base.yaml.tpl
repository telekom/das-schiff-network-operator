# Netplan base configuration template.
# Per-node values are injected by e2e/scripts/configure-underlay.sh.
# Matches vm-lab: per-interface BGP unnumbered, hbn veth trunk to host.
network:
  version: 2
  renderer: networkd
  ethernets:
    hbn:
      addresses:
        - fd00:7:caa5::/127
      mtu: 1500
    eth1:
      accept-ra: false
      link-local: [ipv6]
      mtu: 9100
    eth2:
      accept-ra: false
      link-local: [ipv6]
      mtu: 9100
  dummy-devices:
    dum.underlay:
      addresses:
        - "{{ .VtepIP }}/32"
      mtu: 9100
    lo_calico:
      addresses:
        - "169.254.100.100/32"
        - "fd00:7:caa5:1::/128"
      mtu: 9000
  vrfs:
    mgmt:
      table: 20
      interfaces:
        - br_mgmt
    cluster:
      table: 10
      interfaces:
        - hbn
        - br_cluster
        - lo_calico
  tunnels:
    vx_mgmt:
      mode: vxlan
      mtu: 9000
      link-local: []
      link: dum.underlay
      id: {{ .MgmtVNI }}
      local: "{{ .VtepIP }}"
      port: 4789
      mac-learning: false
    vx_cluster:
      mode: vxlan
      mtu: 9000
      link-local: []
      link: dum.underlay
      id: {{ .ClusterVNI }}
      local: "{{ .VtepIP }}"
      port: 4789
      mac-learning: false
  bridges:
    br_mgmt:
      macaddress: "{{ .MgmtBridgeMAC }}"
      mtu: 9000
      link-local: []
      parameters:
        stp: false
      interfaces:
        - vx_mgmt
    br_cluster:
      macaddress: "{{ .BridgeMAC }}"
      mtu: 9000
      link-local: []
      parameters:
        stp: false
      interfaces:
        - vx_cluster
