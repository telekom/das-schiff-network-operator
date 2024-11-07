{{range $vrf := .VRFs}}
{{if $vrf.IsTaaS}}
router bgp {{$.ASN}} vrf taas.{{$vrf.VNI}}
 bgp router-id {{$.RouterID}}
 !
 address-family ipv4 unicast
  redistribute kernel
 exit-address-family
 !
 address-family ipv6 unicast
  redistribute kernel
 exit-address-family
exit
{{else}}
{{if $vrf.ShouldTemplateVRF}}
router bgp {{$.ASN}} vrf vr.{{$vrf.Name}}
 bgp router-id {{$.RouterID}}
 no bgp suppress-duplicates
 neighbor vd.{{$vrf.Name}} interface remote-as internal
{{range $peering := $vrf.Peerings}}
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} peer-group
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} remote-as {{$peering.RemoteASN}}
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} timers {{$peering.KeepaliveTime}} {{$peering.HoldTime}}
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} maximum-prefix {{$peering.MaximumPrefixes}}
  bgp listen range {{$peering.NeighborRange}} peer-group {{$peering.Name}}{{$peering.AddressFamily}}
{{if $peering.EnableBFD}}
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} bfd
{{end}}
{{end}}
 !
 address-family ipv4 unicast
  neighbor vd.{{$vrf.Name}} activate
  neighbor vd.{{$vrf.Name}} soft-reconfiguration inbound
  neighbor vd.{{$vrf.Name}} route-map rm_{{$vrf.Name}}_export in
  neighbor vd.{{$vrf.Name}} route-map rm_{{$vrf.Name}}_import out
  redistribute connected route-map rm_{{$vrf.Name}}_export
  redistribute kernel route-map rm_{{$vrf.Name}}_export
{{range $item := $vrf.AggregateIPv4}}
  aggregate-address {{$item}}
{{- end }}
{{range $peering := $vrf.Peerings}}
{{if eq $peering.AddressFamily 4}}
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} activate
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-in in
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-out out
{{ else }}
  no neighbor {{$peering.Name}}{{$peering.AddressFamily}} activate
{{end}}
{{end}}
 exit-address-family
 !
 address-family ipv6 unicast
  neighbor vd.{{$vrf.Name}} activate
  neighbor vd.{{$vrf.Name}} soft-reconfiguration inbound
  neighbor vd.{{$vrf.Name}} route-map rm6_{{$vrf.Name}}_export in
  neighbor vd.{{$vrf.Name}} route-map rm6_{{$vrf.Name}}_import out
  redistribute connected route-map rm6_{{$vrf.Name}}_export
  redistribute kernel route-map rm6_{{$vrf.Name}}_export
{{range $item := $vrf.AggregateIPv6}}
  aggregate-address {{$item}}
{{- end }}
{{range $peering := $vrf.Peerings}}
{{if eq $peering.AddressFamily 6}}
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} activate
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-in in
  neighbor {{$peering.Name}}{{$peering.AddressFamily}} prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-out out
{{ else }}
  no neighbor {{$peering.Name}}{{$peering.AddressFamily}} activate
{{end}}
{{- end -}}
 exit-address-family
 !
 address-family l2vpn evpn
  advertise ipv4 unicast route-map rm_{{$vrf.Name}}_export
  advertise ipv6 unicast route-map rm6_{{$vrf.Name}}_export
{{if $vrf.ShouldDefineRT}}
  route-target import {{$vrf.RT}}
  route-target export {{$vrf.RT}}
{{end}}
 exit-address-family
exit
!
{{end}}
{{end}}
{{- end -}}