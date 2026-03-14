frr defaults traditional
hostname {{ .Hostname }}
log syslog informational
service integrated-vtysh-config
!
vrf cluster
 vni 30
 ip route 10.100.0.0/24 blackhole
 ipv6 route fdcb:f93c:3a3e::/64 blackhole
!
vrf mgmt
 vni 20
!
router bgp 64497
 bgp router-id {{ .VtepIP }}
 no bgp suppress-duplicates
 no bgp default ipv4-unicast
 bgp bestpath as-path multipath-relax
 ! Underlay eBGP — per-interface BGP unnumbered
 neighbor eth1 interface remote-as 65500
 neighbor eth1 local-as 65501 no-prepend replace-as
 neighbor eth1 timers 30 90
 neighbor eth2 interface remote-as 65500
 neighbor eth2 local-as 65501 no-prepend replace-as
 neighbor eth2 timers 30 90
 ! EVPN iBGP to route reflectors
 neighbor 192.0.2.1 remote-as 64497
 neighbor 192.0.2.1 timers 30 90
 neighbor 192.0.2.1 update-source dum.underlay
 neighbor 192.0.2.2 remote-as 64497
 neighbor 192.0.2.2 timers 30 90
 neighbor 192.0.2.2 update-source dum.underlay
 !
 address-family ipv4 unicast
  neighbor eth1 activate
  neighbor eth1 allowas-in
  neighbor eth1 route-map TAG-FABRIC-IN in
  neighbor eth1 route-map DENY-TAG-FABRIC-OUT out
  neighbor eth2 activate
  neighbor eth2 allowas-in
  neighbor eth2 route-map TAG-FABRIC-IN in
  neighbor eth2 route-map DENY-TAG-FABRIC-OUT out
  network {{ .VtepIP }}/32
 exit-address-family
 !
 address-family l2vpn evpn
  neighbor 192.0.2.1 activate
  neighbor 192.0.2.1 route-map TAG-FABRIC-IN in
  neighbor 192.0.2.1 route-map DENY-TAG-FABRIC-OUT out
  neighbor 192.0.2.2 activate
  neighbor 192.0.2.2 route-map TAG-FABRIC-IN in
  neighbor 192.0.2.2 route-map DENY-TAG-FABRIC-OUT out
  advertise-all-vni
 exit-address-family
!
router bgp 64497 vrf cluster
 bgp router-id {{ .VtepIP }}
 no bgp suppress-duplicates
 no bgp default ipv4-unicast
 ! kube-vip neighbor via hbn veth
 neighbor fd00:7:caa5::1 remote-as 65170
 neighbor fd00:7:caa5::1 local-as 65169 no-prepend replace-as
 neighbor fd00:7:caa5::1 description kube-vip
 ! Calico neighbors
 neighbor {{ .NodeIPv4 }} remote-as 65170
 neighbor {{ .NodeIPv4 }} local-as 65169 no-prepend replace-as
 neighbor {{ .NodeIPv4 }} update-source 169.254.100.100
 neighbor {{ .NodeIPv4 }} disable-connected-check
 neighbor {{ .NodeIPv4 }} description calicov4
 neighbor {{ .NodeIPv6 }} remote-as 65170
 neighbor {{ .NodeIPv6 }} local-as 65169 no-prepend replace-as
 neighbor {{ .NodeIPv6 }} update-source fd00:7:caa5:1::
 neighbor {{ .NodeIPv6 }} disable-connected-check
 neighbor {{ .NodeIPv6 }} description calicov6
 !
 address-family ipv4 unicast
  redistribute connected
  redistribute static
  redistribute kernel
  import vrf mgmt
  import vrf route-map rm_cluster_import
  neighbor fd00:7:caa5::1 activate
  neighbor fd00:7:caa5::1 prefix-list ANY in
  neighbor {{ .NodeIPv4 }} activate
  neighbor {{ .NodeIPv4 }} prefix-list ANY in
 exit-address-family
 !
 address-family ipv6 unicast
  redistribute connected
  redistribute static
  redistribute kernel
  import vrf mgmt
  import vrf route-map rm_cluster_import
  neighbor fd00:7:caa5::1 activate
  neighbor fd00:7:caa5::1 prefix-list ANY in
  neighbor {{ .NodeIPv6 }} activate
  neighbor {{ .NodeIPv6 }} prefix-list ANY in
 exit-address-family
 !
 address-family l2vpn evpn
  advertise ipv4 unicast route-map rm_export_local
  advertise ipv6 unicast route-map rm_export_local
  route-target import 64497:30
  route-target export 64497:30
 exit-address-family
!
router bgp 64497 vrf mgmt
 bgp router-id {{ .VtepIP }}
 no bgp suppress-duplicates
 no bgp default ipv4-unicast
 !
 address-family ipv4 unicast
  redistribute connected
  redistribute static
  redistribute kernel
  import vrf cluster
  import vrf route-map rm_mgmt_import
 exit-address-family
 !
 address-family ipv6 unicast
  redistribute connected
  redistribute static
  redistribute kernel
  import vrf cluster
  import vrf route-map rm_mgmt_import
 exit-address-family
 !
 address-family l2vpn evpn
  advertise ipv4 unicast route-map rm_export_local
  advertise ipv6 unicast route-map rm_export_local
  route-target import 64497:20
  route-target export 64497:20
 exit-address-family
!
ip prefix-list pl_export_base permit 10.100.0.0/24 le 32
ipv6 prefix-list pl_export_base permit fdcb:f93c:3a3e::/64 le 128
!
ip prefix-list ANY permit any
ipv6 prefix-list ANY permit any
!
ip prefix-list DEFAULT permit 0.0.0.0/0
ipv6 prefix-list DEFAULT permit ::/0
!
ip prefix-list pl_link_local permit 169.254.0.0/16 le 32
ipv6 prefix-list pl_link_local permit fd00:7:caa5::/48 le 128
!
route-map rm_mgmt_import permit 2
 match ip address prefix-list pl_export_base
 match source-vrf cluster
exit
!
route-map rm_mgmt_import permit 3
 match ipv6 address prefix-list pl_export_base
 match source-vrf cluster
exit
!
route-map rm_cluster_import permit 1
 match ip address prefix-list ANY
 set ipv4 vpn next-hop 0.0.0.0
 on-match next
exit
!
route-map rm_cluster_import permit 2
 on-match next
 set local-preference 50
exit
!
route-map rm_cluster_import deny 65533
 match ip address prefix-list DEFAULT
 match source-vrf mgmt
exit
!
route-map rm_cluster_import deny 65534
 match ipv6 address prefix-list DEFAULT
 match source-vrf mgmt
exit
!
route-map rm_cluster_import permit 65535
 match source-vrf mgmt
exit
!
route-map TAG-FABRIC-IN permit 10
 set community 65169:200 additive
 set local-preference 100
exit
!
bgp community-list standard cm-received-fabric permit 65169:200
!
route-map DENY-TAG-FABRIC-OUT deny 10
 match community cm-received-fabric
exit
!
route-map DENY-TAG-FABRIC-OUT permit 20
!
route-map rm_export_local deny 10
 match community cm-received-fabric
exit
!
route-map rm_export_local deny 11
 match ip address prefix-list pl_link_local
exit
!
route-map rm_export_local deny 12
 match ipv6 address prefix-list pl_link_local
exit
!
route-map rm_export_local permit 20
!
bfd
 profile underlay
  detect-multiplier 3
  transmit-interval 333
  receive-interval 333
