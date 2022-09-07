{{range $vrf := .}}
{{range $i, $pl := $vrf.Import}}
{{range $item := $pl.Items}}
{{if $item.IPv6}}
ipv6 prefix-list pl_{{$vrf.Name}}_import_{{$i}} seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- else}}
ip prefix-list pl_{{$vrf.Name}}_import_{{$i}} seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end}}
{{- end -}}
{{- end -}}
{{range $i, $pl := $vrf.Export}}
{{range $item := $pl.Items}}
{{if $item.IPv6}}
ipv6 prefix-list pl_{{$vrf.Name}}_export_{{$i}} seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- else}}
ip prefix-list pl_{{$vrf.Name}}_import_{{$i}} seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end}}
{{- end -}}
{{- end -}}
{{- end -}}