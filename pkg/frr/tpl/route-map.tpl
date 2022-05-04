{{range $vrf := .}}
{{range $i, $pl := $vrf.Import}}
route-map rm_{{$vrf.Name}}_import permit {{$pl.Seq}}
  match ip address prefix-list pl_{{$vrf.Name}}_import_{{$i}}
exit
{{- end -}}
{{range $i, $pl := $vrf.Export}}
route-map rm_{{$vrf.Name}}_export permit {{$pl.Seq}}
  match ip address prefix-list pl_{{$vrf.Name}}_export_{{$i}}
exit
{{- end -}}
{{- end -}}