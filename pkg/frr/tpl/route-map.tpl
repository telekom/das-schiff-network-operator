{{range $vrf := .}}
{{range $i, $pl := $vrf.Import}}
route-map rm4_{{$vrf.Name}}_import permit {{$pl.Seq}}
  match ip address prefix-list pl_{{$vrf.Name}}_import_{{$i}}
exit
route-map rm6_{{$vrf.Name}}_import permit {{$pl.Seq}}
  match ipv6 address prefix-list pl_{{$vrf.Name}}_import_{{$i}}
exit
{{- end -}}
{{range $i, $pl := $vrf.Export}}
route-map rm4_{{$vrf.Name}}_export permit {{$pl.Seq}}
  match ip address prefix-list pl_{{$vrf.Name}}_export_{{$i}}
{{if $pl.Community}}
  set community $pl.Community
{{- end}}
exit
route-map rm6_{{$vrf.Name}}_export permit {{$pl.Seq}}
  match ipv6 address prefix-list pl_{{$vrf.Name}}_export_{{$i}}
{{if $pl.Community}}
  set community $pl.Community
{{- end}}
exit
{{- end -}}
{{- end -}}