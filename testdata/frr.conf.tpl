frr version 8.5.1
frr defaults traditional
hostname t14s
log file /var/log/frr/frr.log
log stdout informational
log syslog informational
vni 1000
ip protocol bgp route-map rm_set_nodeip_source
ip route 0.0.0.2/32 blackhole
ip route 172.30.64.0/18 lo
ip route 169.254.1.1/32 lo
!
vrf Vrf_mgmt
  vni 20
exit-vrf
{{.VRFs}}
!
service integrated-vtysh-config
!
router bgp 64510 vrf Vrf_underlay
  bgp router-id 192.0.2.128
  no bgp ebgp-requires-policy
  no bgp suppress-duplicates
  no bgp default ipv4-unicast
  bgp bestpath as-path multipath-relax
  neighbor leaf peer-group
  neighbor leaf remote-as external
  neighbor leaf timers 1 3
  neighbor leaf bfd
  neighbor ens1f0 interface peer-group leaf
  neighbor ens1f1 interface peer-group leaf
  neighbor ens2f0 interface peer-group leaf
  neighbor ens2f1 interface peer-group leaf
  !
  address-family ipv4 unicast
    network 192.0.2.128/32
    neighbor leaf activate
    neighbor leaf allowas-in origin
    neighbor leaf soft-reconfiguration inbound
    neighbor leaf route-map TAG-FABRIC-IN in
    neighbor leaf route-map DENY-TAG-FABRIC-OUT out
  exit-address-family
  !
  address-family l2vpn evpn
    neighbor leaf activate
    neighbor leaf allowas-in origin
    neighbor leaf soft-reconfiguration inbound
    neighbor leaf route-map TAG-FABRIC-IN in
    neighbor leaf route-map DENY-TAG-FABRIC-OUT out
    advertise-all-vni
    vni 30
      advertise-svi-ip
    exit-vni
    advertise-svi-ip
  exit-address-family
exit
!
router bgp 64510 vrf default
  bgp router-id 198.51.100.0
  no bgp suppress-duplicates
  neighbor 127.0.0.1 remote-as 64511
  neighbor 127.0.0.1 port 1790
  neighbor 127.0.0.1 passive
  neighbor 127.0.0.1 update-source lo
  neighbor def_mgmt interface remote-as internal
{{.Neighbors}}
  !
  address-family ipv4 unicast
    redistribute connected
    neighbor 127.0.0.1 soft-reconfiguration inbound
    neighbor 127.0.0.1 route-map local_peering_hack in
    neighbor def_mgmt activate
    neighbor def_mgmt allowas-in origin
    neighbor def_mgmt soft-reconfiguration inbound
    neighbor def_mgmt route-map rm_import_cluster_to_oam out
    neighbor def_mgmt route-map rm_import_Vrf_mgmt_to_cluster in
    import vrf Vrf_coil
    import vrf Vrf_nwop
    import vrf Vrf_kubevip
{{.NeighborsV4}}
  exit-address-family
  !
  address-family l2vpn evpn
    advertise ipv4 unicast route-map rm_export_default
  exit-address-family
exit
!
router bgp 64510 vrf Vrf_mgmt
  bgp router-id 192.0.2.128
  no bgp suppress-duplicates
  neighbor mgmt_def interface remote-as internal
  !
  address-family ipv4 unicast
    neighbor mgmt_def activate
    neighbor mgmt_def soft-reconfiguration inbound
    neighbor mgmt_def route-map rm_import_cluster_to_oam in
  exit-address-family
  !
  address-family l2vpn evpn
    advertise ipv4 unicast route-map rm_import_cluster_to_oam
  exit-address-family
exit
!
router bgp 64510 vrf Vrf_coil
  bgp router-id 192.0.2.128
  !
  address-family ipv4 unicast
    redistribute kernel
  exit-address-family
exit
!
router bgp 64510 vrf Vrf_nwop
  bgp router-id 192.0.2.128
  !
  address-family ipv4 unicast
    redistribute kernel
  exit-address-family
exit
!
router bgp 64510 vrf Vrf_kubevip
  bgp router-id 192.0.2.128
  !
  address-family ipv4 unicast
    redistribute kernel
  exit-address-family
exit
!
{{.BGP}}
!
ip prefix-list pl_node_network seq 10 permit 198.51.100.0/27 le 32
route-map rm_export_default permit 10
  match ip address prefix-list pl_node_network
exit
route-map rm_export_default permit 20
  match source-vrf Vrf_coil
exit
!
route-map TAG-FABRIC-IN permit 10
  set tag 20000
exit
!
route-map DENY-TAG-FABRIC-OUT deny 10
  match tag 20000
exit
!
route-map DENY-TAG-FABRIC-OUT permit 20
exit
!
route-map rm_import_cluster_to_oam permit 10
  match ip address prefix-list pl_node_network
exit
route-map rm_import_cluster_to_oam permit 20
  call rm_om_refm2m_export
exit
!
route-map rm_om_refm2m_export deny 65535
exit
!
ip prefix-list pl_import_Vrf_mgmt_to_cluster seq 10 permit 0.0.0.0/0 le 32
route-map rm_import_Vrf_mgmt_to_cluster permit 10
  match ip address prefix-list pl_import_Vrf_mgmt_to_cluster
exit
!
route-map local_peering_hack permit 10
  set as-path exclude 64511
  set ip next-hop 0.0.0.2
exit
!
ip prefix-list ANY permit 0.0.0.0/0 le 32
route-map rm_set_nodeip_source permit 10
  match ip address prefix-list ANY
  set src 198.51.100.0
exit
!
# Automatically configured Prefix-Lists and Route-Maps
{{.PrefixLists}}
{{.RouteMaps}}
!
line vty
!
