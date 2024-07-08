{{ if .IPv4MgmtRouteMapIn }}
route-map {{ .IPv4MgmtRouteMapIn }} permit 5
  call rm_{{ .MgmtVrfName }}_import
  on-match next
exit
route-map rm_{{ .MgmtVrfName }}_import permit 65535
exit
{{- end }}
{{ if .IPv6MgmtRouteMapIn }}
route-map {{ .IPv6MgmtRouteMapIn }} permit 5
  call rm6_{{ .MgmtVrfName }}_import
  on-match next
exit
route-map rm6_{{ .MgmtVrfName }}_import permit 65535
exit
{{- end }}