{{range $vrf := .}}
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
{{- end -}}