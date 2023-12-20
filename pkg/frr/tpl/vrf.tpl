{{range $vrf := .}}
{{if and $vrf.ShouldTemplateVRF (not $vrf.IsTaaS)}}
vrf vr.{{$vrf.Name}}
 vni {{$vrf.VNI}}
exit-vrf
{{end}}
{{- end -}}