{{range $vrf := .VRFs}}
{{if not $vrf.IsTaaS}}
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
ip prefix-list pl_{{$vrf.Name}}_export_{{$i}} seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{range $vrf := .VRFs}}
{{range $peering := $vrf.Peerings}}
{{if eq $peering.AddressFamily 4}}
{{range $item := $peering.Import}}
{{if not $item.IPv6}}
ip prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-in seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end -}}
{{- end -}}
{{range $item := $peering.Export}}
{{if not $item.IPv6}}
ip prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-out seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end -}}
{{- end -}}
{{- else}}
{{range $item := $peering.Import}}
{{if $item.IPv6}}
ipv6 prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-in seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end -}}
{{- end -}}
{{range $item := $peering.Export}}
{{if $item.IPv6}}
ipv6 prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-out seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{range $peering := .DefaultVRFBGPPeerings}}
{{if eq $peering.AddressFamily 4}}
{{range $item := $peering.Import}}
{{if not $item.IPv6}}
ip prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-in seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end -}}
{{- end -}}
{{range $item := $peering.Export}}
{{if not $item.IPv6}}
ip prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-out seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end -}}
{{- end -}}
{{- else}}
{{range $item := $peering.Import}}
{{if $item.IPv6}}
ipv6 prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-in seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end -}}
{{- end -}}
{{range $item := $peering.Export}}
{{if $item.IPv6}}
ipv6 prefix-list bgpaas-{{$peering.Name}}{{$peering.AddressFamily}}-out seq {{$item.Seq}} {{$item.Action}} {{$item.CIDR}}{{if $item.GE}} ge {{$item.GE}}{{end}}{{if $item.LE}} le {{$item.LE}}{{end}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
