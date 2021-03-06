{{range $vrf := .}}
{{if $vrf.ShouldTemplateVRF}}
  neighbor dv.{{$vrf.Name}} allowas-in origin
  neighbor dv.{{$vrf.Name}} route-map rm_{{$vrf.Name}}_import in
  neighbor dv.{{$vrf.Name}} route-map rm_{{$vrf.Name}}_export out
{{end}}
{{- end -}}