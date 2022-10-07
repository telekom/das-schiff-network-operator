{{range $vrf := .VRFs}}
{{if $vrf.ShouldTemplateVRF}}
router bgp {{$.ASN}} vrf vr.{{$vrf.Name}}
 bgp router-id {{$.RouterID}}
 no bgp suppress-duplicates
 neighbor vd.{{$vrf.Name}} interface remote-as internal
 !
 address-family ipv4 unicast
  neighbor vd.{{$vrf.Name}} soft-reconfiguration inbound
  neighbor vd.{{$vrf.Name}} route-map rm4_{{$vrf.Name}}_export in
  neighbor vd.{{$vrf.Name}} route-map rm4_{{$vrf.Name}}_import out
 exit-address-family
 !
 address-family ipv6 unicast
  neighbor vd.{{$vrf.Name}} soft-reconfiguration inbound
  neighbor vd.{{$vrf.Name}} route-map rm6_{{$vrf.Name}}_export in
  neighbor vd.{{$vrf.Name}} route-map rm6_{{$vrf.Name}}_import out
 exit-address-family
 !
 address-family l2vpn evpn
  advertise ipv4 unicast route-map rm4_{{$vrf.Name}}_export
  advertise ipv6 unicast route-map rm6_{{$vrf.Name}}_export
{{if $vrf.RT}}
  rt vpn import $vrf.RT
  rt vpn export $vrf.RT
{{end}}
 exit-address-family
exit
!
{{end}}
{{- end -}}