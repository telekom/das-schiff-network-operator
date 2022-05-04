{{range $vrf := .}}
{{if $vrf.ShouldTemplateVRF}}
 neighbor dv.{{$vrf.Name}} interface remote-as internal
{{end}}
{{- end -}}