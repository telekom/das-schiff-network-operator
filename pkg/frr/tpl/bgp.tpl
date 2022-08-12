{{range $vrf := .VRFs}}
{{if $vrf.ShouldTemplateVRF}}
router bgp {{$.ASN}} vrf vr.{{$vrf.Name}}
 bgp router-id {{$.RouterID}}
 no bgp suppress-duplicates
 neighbor vd.{{$vrf.Name}} interface remote-as internal
 !
 address-family ipv4 unicast
  neighbor vd.{{$vrf.Name}} soft-reconfiguration inbound
  neighbor vd.{{$vrf.Name}} route-map rm_{{$vrf.Name}}_export in
  neighbor vd.{{$vrf.Name}} route-map rm_{{$vrf.Name}}_import out
 exit-address-family
 !
 address-family l2vpn evpn
  advertise ipv4 unicast route-map rm_{{$vrf.Name}}_export
 exit-address-family
exit
!
{{end}}
{{- end -}}