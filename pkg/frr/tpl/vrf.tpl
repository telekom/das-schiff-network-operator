{{range $vrf := .}}
{{if $vrf.ShouldTemplateVRF}}
vrf vr.{{$vrf.Name}}
 vni {{$vrf.VNI}}
exit-vrf
{{end}}
{{range $item := $vrf.StaticRoutesIPv4}}
ip route {{$item}} 169.254.0.1 {{$vrf.StaticRouteInterface}}
{{- end }}
{{range $item := $vrf.StaticRoutesIPv6}}
ipv6 route {{$item}} {{$vrf.StaticRouteIPv6NextHop}} {{$vrf.StaticRouteInterface}}
{{- end }}
{{- end -}}