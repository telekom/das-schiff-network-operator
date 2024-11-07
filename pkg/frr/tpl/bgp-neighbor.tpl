{{range $vrf := .VRFs}}
{{if and $vrf.ShouldTemplateVRF (not $vrf.IsTaaS)}}
 neighbor dv.{{$vrf.Name}} interface remote-as internal
{{end}}
{{- end -}}
{{range $peering := .DefaultVRFBGPPeerings}}
 neighbor {{$peering.Name}}{{$peering.AddressFamily}} peer-group
 neighbor {{$peering.Name}}{{$peering.AddressFamily}} remote-as {{$peering.RemoteASN}}
 neighbor {{$peering.Name}}{{$peering.AddressFamily}} timers {{$peering.KeepaliveTime}} {{$peering.HoldTime}}
 neighbor {{$peering.Name}}{{$peering.AddressFamily}} maximum-prefix {{$peering.MaximumPrefixes}}
 bgp listen range {{$peering.NeighborRange}} peer-group {{$peering.Name}}{{$peering.AddressFamily}}
{{if $peering.EnableBFD}}
 neighbor {{$peering.Name}}{{$peering.AddressFamily}} bfd
{{end}}
{{end}}