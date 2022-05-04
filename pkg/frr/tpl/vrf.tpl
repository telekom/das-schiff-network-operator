{{range $vrf := .}}
{{if $vrf.ShouldTemplateVRF}}
vrf vr.{{$vrf.Name}}
 vni {{$vrf.VNI}}
exit-vrf
{{end}}
{{- end -}}