{{range $vrf := .VRFs}}
{{if and $vrf.ShouldTemplateVRF (not $vrf.IsTaaS)}}
 neighbor dv.{{$vrf.Name}} interface remote-as internal
{{end}}
{{- end -}}