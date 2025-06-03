{{range $vrf := .VRFs}}
{{if and $vrf.ShouldTemplateVRF (not $vrf.IsTaaS)}}
  neighbor dv.{{$vrf.Name}} activate
  neighbor dv.{{$vrf.Name}} allowas-in origin
  neighbor dv.{{$vrf.Name}} soft-reconfiguration inbound
  neighbor dv.{{$vrf.Name}} route-map rm6_{{$vrf.Name}}_import in
  neighbor dv.{{$vrf.Name}} route-map rm6_{{$vrf.Name}}_export out
{{- else }}
{{range $item := $vrf.AggregateIPv6}}
  aggregate-address {{$item}}
{{- end }}
{{- end }}
{{if $vrf.IsTaaS}}
  import vrf taas.{{$vrf.VNI}}
{{- end }}
{{- end }}
{{range $peering := .DefaultVRFBGPPeerings}}
{{if eq $peering.AddressFamily 6}}
neighbor {{$peering.Name}}{{$peering.AddressFamily}} activate
neighbor {{$peering.Name}}{{$peering.AddressFamily}} soft-reconfiguration inbound
neighbor {{$peering.Name}}{{$peering.AddressFamily}} maximum-prefix {{$peering.MaximumPrefixes}}
neighbor {{$peering.Name}}{{$peering.AddressFamily}} prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-in in
neighbor {{$peering.Name}}{{$peering.AddressFamily}} prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-out out
{{ else }}
no neighbor {{$peering.Name}}{{$peering.AddressFamily}} activate
{{end}}
{{end}}