{{- /*gotype:github.com/telekom/das-schiff-network-operator/pkg/cra.frrTemplateData*/ -}}
{{ define "staticRoutes" }}
{{ range $route := . }}
{{ if $route.NextHop }}
{{ if $route.NextHop.Address }}
{{ if isIPv4 $route.Prefix }}ip{{ else }}ipv6{{ end }} route {{ $route.Prefix }} {{ $route.NextHop.Address }}
{{ end }}
{{ if $route.NextHop.Vrf }}
{{ if isIPv4 $route.Prefix }}ip{{ else }}ipv6{{ end }} route {{ $route.Prefix }} {{ if isIPv4 $route.Prefix }}0.0.0.0{{ else }}::{{ end }} nexthop-vrf {{ $route.NextHop.Vrf }}
{{ end }}
{{ else }}
{{ if isIPv4 $route.Prefix }}ip{{ else }}ipv6{{ end }} route {{ $route.Prefix }} blackhole
{{ end }}
{{ end }}
{{ end }}

{{ define "rmAction" }}{{ if eq .Type "accept" }}permit{{ else }}deny{{ end }}{{ end }}

{{ define "prefixListEntry" }}
{{ if isIPv4 .Matcher.Prefix }}ip{{ else }}ipv6{{ end }} prefix-list {{ .Name }}{{ if .Seq }} seq {{ .Seq }}{{ end }} {{ .Action }} {{ .Matcher.Prefix }}{{ if .Matcher.Le }} le {{ .Matcher.Le }}{{ end }}{{ if .Matcher.Ge }} ge {{ .Matcher.Ge }}{{ end }}
{{ end }}

{{ define "filter" }}
{{ $param := . }}
{{ range $j, $item := $param.Filter.Items }}
{{ if $item.Matcher.Prefix }}
{{ template "prefixListEntry" dict "Matcher" $item.Matcher.Prefix "Name" (printf "pl_%s_%d" $param.Name $j) "Action" "permit" "Seq" 5 }}
{{ end }}
{{ if $item.Matcher.BGPCommunity }}
bgp community-list standard cm_{{ $param.Name }}_{{ $j }} permit {{ $item.Matcher.BGPCommunity }}
{{ end }}
{{ if $item.Action.ModifyRoute }}
{{ if $item.Action.ModifyRoute.RemoveCommunities }}
{{ range $com := $item.ModifyRoute.RemoveCommunities }}
bgp community-list standard cm_remove_{{ $param.Name }}_{{ $j }} permit {{ $com }}
{{ end }}
{{ end }}
{{ end }}
!
route-map rm_{{ $param.Name }} {{ template "rmAction" $item.Action }} {{ add $j 10 }}
{{ if $item.Matcher.Prefix }}
{{ if isIPv4 $item.Matcher.Prefix.Prefix }}
match ip address prefix-list pl_{{ $param.Name }}_{{ $j }}
{{ else }}
match ipv6 address prefix-list pl_{{ $param.Name }}_{{ $j }}
{{ end }}
{{ end }}
{{ if $item.Matcher.BGPCommunity }}
match community cm_{{ $param.Name }}_{{ $j }}
{{ end }}
{{ if $item.Action.ModifyRoute }}
{{ if $item.Action.ModifyRoute }}
{{ if $item.Action.ModifyRoute.AddCommunities }}
set community {{ join $item.MAction.odifyRoute.AddCommunities " " }}{{ if $item.Action.ModifyRoute.AdditiveCommunities }} additive{{ end }}
{{ end }}
{{ if $item.Action.ModifyRoute.RemoveCommunities }}
set comm-list cm_remove_{{ $param.Name }}_{{ $j }} delete
{{ end }}
{{ if $item.Action.ModifyRoute.RemoveAllCommunities }}
set community none
{{ end }}
{{ if eq $item.Action.Type "next" }}
on-match next
{{ end }}
{{ end }}
{{ end }}
end
{{ end }}
route-map rm_{{ $param.Name }} {{ template "rmAction" .Filter.DefaultAction }} {{ add (len .Filter.Items) 10 }}
end
{{ end }}

{{ define "vrfFilters" }}
{{ $param := . }}
{{/* Filters for VRF imports */}}
{{ range $i, $import := $param.Imports }}
route-map rm_{{ $param.Vrf }}_import permit {{ add $i 10 }}
match source-vrf {{ $import.FromVRF }}
call rm_{{ $param.Vrf }}_import_{{ $import.FromVRF }}
end
!
{{ template "filter" dict "Filter" $import.Filter "Name" (printf "%s_import_%s" $param.Vrf $import.FromVRF) }}
{{ end }}

{{/* Filters for VRF peers */}}
{{ range $peer := .BGPPeers }}
{{ $safeName := include "userPeerSafeName" . }}
{{ if and $peer.IPv4 $peer.IPv4.ExportFilter }}
{{ template "filter" dict "Filter" $peer.IPv4.ExportFilter "Name" (printf "%s-ipv4-out" $safeName) }}
{{ end }}
{{ if and $peer.IPv4 $peer.IPv4.ImportFilter }}
{{ template "filter" dict "Filter" $peer.IPv4.ImportFilter "Name" (printf "%s-ipv4-in" $safeName) }}
{{ end }}
{{ if and $peer.IPv6 $peer.IPv6.ExportFilter }}
{{ template "filter" dict "Filter" $peer.IPv6.ExportFilter "Name" (printf "%s-ipv6-out" $safeName) }}
{{ end }}
{{ if and $peer.IPv6 $peer.IPv6.ImportFilter }}
{{ template "filter" dict "Filter" $peer.IPv6.ImportFilter "Name" (printf "%s-ipv6-in" $safeName) }}
{{ end }}
{{ end }}
{{ end }}

{{ define "peerIdentifier" }}{{ if .IP }}{{ .IP }}{{ else }}{{ .Interface }}{{ end }}{{ end }}
{{ define "userPeerIdentifier" }}{{ if .Address }}{{ .Address }}{{ else }}{{ .ListenRange | hash }}{{ end }}{{ end }}
{{ define "userPeerSafeName" }}{{ if .Address }}{{ .Address | hash }}{{ else }}{{ .ListenRange | hash }}{{ end }}{{ end }}

{{ define "peerStatement" }}
{{ if .IP }}
neighbor {{ .IP }} remote-as {{ .RemoteASN }}
{{ else }}
neighbor {{ .Interface }} interface remote-as {{ .RemoteASN }}
{{ end }}
{{ end }}

{{ define "bgpNeighbor" }}
{{ $peerIdentifier := include "userPeerIdentifier" . }}
{{ $safeName := include "userPeerSafeName" . }}
{{ if .Address }}
neighbor {{ $peerIdentifier }} remote-as {{ .RemoteASN }}
{{ else if .ListenRange }}
neighbor {{ $peerIdentifier }} peer-group
neighbor {{ $peerIdentifier }} remote-as {{ .RemoteASN }}
bgp listen range {{ .ListenRange }} peer-group {{ $peerIdentifier }}
{{ end }}
neighbor {{ $peerIdentifier }} timers {{ .KeepaliveTime.Seconds }} {{ .HoldTime.Seconds }}
{{ if .Multihop }}
neighbor {{ $peerIdentifier }} ttl-security hops {{ .Multihop }}
{{ end }}

{{ if .IPv4 }}
address-family ipv4 unicast
  neighbor {{ $peerIdentifier }} activate
  {{ if .IPv4.MaxPrefixes }}
  neighbor {{ $peerIdentifier }} maximum-prefix {{ .IPv4.MaxPrefixes }}
  {{ end }}
  {{ if .IPv4.ImportFilter }}
  neighbor {{ $peerIdentifier }} route-map {{ $safeName }}-ipv4-in in
  {{ end }}
  {{ if .IPv4.ExportFilter }}
  neighbor {{ $peerIdentifier }} route-map {{ $safeName }}-ipv4-out out
  {{ end }}
exit-address-family
{{ end }}


{{ if .IPv6 }}
address-family ipv4 unicast
  neighbor {{ $peerIdentifier }} activate
  {{ if .IPv6.MaxPrefixes }}
  neighbor {{ $peerIdentifier }} maximum-prefix {{ .IPv6.MaxPrefixes }}
  {{ end }}
  {{ if .IPv6.ImportFilter }}
  neighbor {{ $peerIdentifier }} route-map {{ $safeName }}-ipv6-in in
  {{ end }}
  {{ if .IPv6.ExportFilter }}
  neighbor {{ $peerIdentifier }} route-map {{ $safeName }}-ipv6-out out
  {{ end }}
exit-address-family
{{ end }}
{{ end }}
!

{{ define "bgpBaseNeighbor" }}
{{ $peer := .Peer }}
{{ $isUnderlay := .IsUnderlay }}
{{ $peerIdentifier := include "peerIdentifier" $peer }}
{{ template "peerStatement" $peer }}
{{ if $peer.LocalASN }}
neighbor {{ $peerIdentifier }} local-as {{ $peer.LocalASN }} no-prepend replace-as
{{ end }}
neighbor {{ $peerIdentifier }} timers {{ $peer.KeepaliveTime }} {{ $peer.HoldTime }}
{{ if $peer.UpdateSource }}
neighbor {{ $peerIdentifier }} update-source {{ $peer.UpdateSource }}
neighbor {{ $peerIdentifier }} disable-connected-check
{{ end }}
{{ if $peer.IPv4 }}
address-family ipv4 unicast
neighbor {{ $peerIdentifier }} activate
neighbor {{ $peerIdentifier }} allowas-in
{{ if $isUnderlay }}
neighbor {{ $peerIdentifier }} route-map TAG-FABRIC-IN in
neighbor {{ $peerIdentifier }} route-map DENY-TAG-FABRIC-OUT out
{{ else }}
neighbor {{ $peerIdentifier }} prefix-list ANY in
{{ end }}
exit-address-family
{{ end }}
{{ if and $peer.EVPN $isUnderlay }}
address-family l2vpn evpn
neighbor {{ $peerIdentifier }} activate
{{ if $isUnderlay }}
neighbor {{ $peerIdentifier }} route-map TAG-FABRIC-IN in
neighbor {{ $peerIdentifier }} route-map DENY-TAG-FABRIC-OUT out
{{ end }}
exit-address-family
{{ end }}
{{ if $peer.IPv6 }}
address-family ipv6 unicast
neighbor {{ $peerIdentifier }} activate
{{ if $isUnderlay }}
neighbor {{ $peerIdentifier }} route-map TAG-FABRIC-IN in
neighbor {{ $peerIdentifier }} route-map DENY-TAG-FABRIC-OUT out
{{ else }}
neighbor {{ $peerIdentifier }} prefix-list ANY in
{{ end }}
exit-address-family
{{ end }}
{{ end }}
!
frr version 8.0.1
frr defaults traditional
log file /var/log/frr/frr.log
log stdout informational
log syslog informational
!
vrf cluster
  vni {{ $.Config.ClusterVRF.VNI }}
  {{ if $.NodeConfig.ClusterVRF }}
  {{ template "staticRoutes" $.NodeConfig.ClusterVRF.StaticRoutes }}
  {{ end }}
  {{ range $exportCIDR := $.Config.ExportCIDRs }}
  {{ if isIPv4 $exportCIDR }}ip{{ else }}ipv6{{ end }} route {{ $exportCIDR }} blackhole
  {{ end }}
exit-vrf
!
vrf {{ $.Config.ManagementVRF.Name }}
  vni {{ $.Config.ManagementVRF.VNI }}
  {{ if (containsKey $.NodeConfig.FabricVRFs $.Config.ManagementVRF.Name) }}
  {{ $vrf := index $.NodeConfig.FabricVRFs $.Config.ManagementVRF.Name }}
  {{ template "staticRoutes" $vrf.StaticRoutes }}
  {{ end }}
exit-vrf
!
{{ range $name, $vrf := $.NodeConfig.FabricVRFs }}
{{ if not (eq $name $.Config.ManagementVRF.Name) }}
vrf {{ $name }}
  vni {{ $vrf.VNI }}
  {{ template "staticRoutes" $vrf.StaticRoutes }}
exit-vrf
!
router bgp {{ $.Config.LocalASN }} vrf {{ $name }}
  bgp router-id {{ $.Config.VTEPLoopbackIP }}
  no bgp default ipv4-unicast
  no bgp suppress-duplicates
  {{ range $peer := $vrf.BGPPeers }}
  {{ template "bgpNeighbor" $peer }}
  {{ end }}

  address-family ipv4 unicast
    redistribute connected
    redistribute static
    redistribute kernel
    {{ range $vrfImport := $vrf.VRFImports }}
    import vrf {{ $vrfImport.FromVRF }}
    {{ end }}
	  import vrf route-map rm_{{ $name }}_import
  exit-address-family

  address-family ipv6 unicast
    redistribute connected
    redistribute static
    redistribute kernel
    {{ range $vrfImport := $vrf.VRFImports }}
    import vrf {{ $vrfImport.FromVRF }}
    {{ end }}
    import vrf route-map rm_{{ $name }}_import
  exit-address-family

  address-family l2vpn evpn
    {{ range $rt := $vrf.EVPNImportRouteTargets }}
    route-target import {{ $rt }}
    {{ end }}
    {{ range $rt := $vrf.EVPNExportRouteTargets }}
    route-target export {{ $rt }}
    {{ end }}
    advertise ipv4 unicast route-map rm_export_local
    advertise ipv6 unicast route-map rm_export_local
  exit-address-family
exit
!
{{ template "vrfFilters" dict "Vrf" $name "Imports" $vrf.VRFImports "BGPPeers" $vrf.BGPPeers }}
{{ end }}
{{ end }}
!
{{ range $name, $vrf := $.NodeConfig.LocalVRFs }}
{{ if not (eq $name $.Config.ManagementVRF.Name) }}
vrf {{ $name }}
  {{ template "staticRoutes" $vrf.StaticRoutes }}
exit-vrf
!
router bgp {{ $.Config.LocalASN }} vrf {{ $name }}
  bgp router-id {{ $.Config.VTEPLoopbackIP }}
  no bgp suppress-duplicates
  {{ range $peer := $vrf.BGPPeers }}
  {{ template "bgpNeighbor" $peer }}
  {{ end }}
  address-family ipv4 unicast
    redistribute connected
    redistribute static
    redistribute kernel
  exit-address-family

  address-family ipv6 unicast
    redistribute connected
    redistribute static
    redistribute kernel
  exit-address-family
exit
!
{{ template "vrfFilters" dict "Vrf" $name "Imports" $vrf.VRFImports "BGPPeers" $vrf.BGPPeers }}
{{ end }}
{{ end }}
!
router bgp {{ $.Config.LocalASN }}
  bgp router-id {{ $.Config.VTEPLoopbackIP }}
  no bgp ebgp-requires-policy
  no bgp suppress-duplicates
  no bgp default ipv4-unicast
  bgp bestpath as-path multipath-relax

  {{ range $peer := $.Config.UnderlayNeighbors }}
  {{ template "bgpBaseNeighbor" dict "Peer" $peer "IsUnderlay" true }}
  {{ end }}

  address-family ipv4 unicast
    network {{ $.Config.VTEPLoopbackIP }}/32
  exit-address-family
  !
  address-family l2vpn evpn
    advertise-all-vni
    {{ range $layer2 := $.NodeConfig.Layer2s }}
	  {{ if $layer2.RouteTarget }}
	  vni {{ $layer2.VNI }}
	    route-target export {{ $layer2.RouteTarget }}
      route-target import {{ $layer2.RouteTarget }}
    exit-vni
    {{ end }}
	  {{ end }}
  exit-address-family
exit
!
router bgp {{ $.Config.LocalASN }} vrf cluster
  bgp router-id {{ $.Config.VTEPLoopbackIP }}
  no bgp suppress-duplicates
  no bgp default ipv4-unicast

  address-family ipv4 unicast
    redistribute connected
    redistribute static
    redistribute kernel
    {{ if $.NodeConfig.ClusterVRF }}
    {{ range $vrfImport := $.NodeConfig.ClusterVRF.VRFImports }}
    {{ if ne $vrfImport.FromVRF $.Config.ManagementVRF.Name }}
	  import vrf {{ $vrfImport.FromVRF }}
    {{ end }}
    {{ end }}
	  {{ end }}
	  import vrf {{ $.Config.ManagementVRF.Name }}
	  import vrf route-map rm_cluster_import
  exit-address-family

  address-family ipv6 unicast
    redistribute connected
    redistribute static
	  redistribute kernel
	  {{ if $.NodeConfig.ClusterVRF }}
    {{ range $vrfImport := $.NodeConfig.ClusterVRF.VRFImports }}
    {{ if ne $vrfImport.FromVRF $.Config.ManagementVRF.Name }}
    import vrf {{ $vrfImport.FromVRF }}
	  {{ end }}
    {{ end }}
	  {{ end }}
    import vrf {{ $.Config.ManagementVRF.Name }}
    import vrf route-map rm_cluster_import
  exit-address-family

  address-family l2vpn evpn
    advertise ipv4 unicast route-map rm_export_local
    advertise ipv6 unicast route-map rm_export_local
	  route-target import {{ $.Config.ClusterVRF.EVPNRouteTarget }}
    route-target export {{ $.Config.ClusterVRF.EVPNRouteTarget }}
  exit-address-family

  {{ if $.NodeConfig.ClusterVRF }}
  {{ range $peer := $.NodeConfig.ClusterVRF.BGPPeers }}
  {{ template "bgpNeighbor" $peer }}
  {{ end }}
  {{ end }}


{{ range $peer := $.Config.ClusterNeighbors }}
{{ template "bgpBaseNeighbor" dict "Peer" $peer "IsUnderlay" false }}
{{ end }}
exit
!
{{ if $.NodeConfig.ClusterVRF }}
{{ template "vrfFilters" dict "Vrf" "cluster" "Imports" $.NodeConfig.ClusterVRF.VRFImports "BGPPeers" $.NodeConfig.ClusterVRF.BGPPeers }}
{{ end }}
!
router bgp {{ $.Config.LocalASN }} vrf {{ $.Config.ManagementVRF.Name }}
  bgp router-id {{ $.Config.VTEPLoopbackIP }}
  no bgp suppress-duplicates
  no bgp default ipv4-unicast

  address-family ipv4 unicast
    redistribute connected
    redistribute static
  exit-address-family

  address-family ipv6 unicast
    redistribute connected
    redistribute static
  exit-address-family

  address-family l2vpn evpn
    advertise ipv4 unicast route-map rm_export_local
    advertise ipv6 unicast route-map rm_export_local
    route-target import {{ $.Config.ManagementVRF.EVPNRouteTarget }}
    route-target export {{ $.Config.ManagementVRF.EVPNRouteTarget }}
  exit-address-family

  address-family ipv4 unicast
    {{ if containsKey $.NodeConfig.FabricVRFs $.Config.ManagementVRF.Name }}
    {{ $vrf := index $.NodeConfig.FabricVRFs $.Config.ManagementVRF.Name }}
    {{ range $vrfImport := $vrf.VRFImports }}
	  {{ if ne $vrfImport.FromVRF "cluster" }}
    import vrf {{ $vrfImport.FromVRF }}
	  {{ end }}
    {{ end }}
    {{ end }}
	  import vrf cluster
    import vrf route-map rm_{{ $.Config.ManagementVRF.Name }}_import
  exit-address-family

  address-family ipv6 unicast
    {{ if containsKey $.NodeConfig.FabricVRFs $.Config.ManagementVRF.Name }}
    {{ $vrf := index $.NodeConfig.FabricVRFs $.Config.ManagementVRF.Name }}
    {{ range $vrfImport := $vrf.VRFImports }}
    {{ if ne $vrfImport.FromVRF "cluster" }}
    import vrf {{ $vrfImport.FromVRF }}
    {{ end }}
    {{ end }}
    {{ end }}
    import vrf cluster
    import vrf route-map rm_{{ $.Config.ManagementVRF.Name }}_import
  exit-address-family
exit
!
{{ range $exportCIDR := $.Config.ExportCIDRs }}
{{ if isIPv4 $exportCIDR }}ip{{ else }}ipv6{{ end }} prefix-list pl_export_base permit {{ $exportCIDR }} le {{ if isIPv4 $exportCIDR }}32{{ else }}128{{ end }}
{{ end }}
!
ip prefix-list pl_link_local permit 169.254.0.0/16 le 32
ipv6 prefix-list pl_link_local permit fd00:7:caa5::/48 le 128
!
ip prefix-list ANY permit any
ipv6 prefix-list ANY permit any
!
ip prefix-list DEFAULT permit 0.0.0.0/0
ipv6 prefix-list DEFAULT permit ::/0
!
route-map rm_{{ $.Config.ManagementVRF.Name }}_import permit 1
  match ip address prefix-list ANY
  set ipv4 vpn next-hop 0.0.0.0
  on-match next
exit
route-map rm_{{ $.Config.ManagementVRF.Name }}_import permit 2
  match ip address prefix-list pl_export_base
  match source-vrf cluster
exit
route-map rm_{{ $.Config.ManagementVRF.Name }}_import permit 3
  match ipv6 address prefix-list pl_export_base
  match source-vrf cluster
exit
!
route-map rm_cluster_import permit 1
  match ip address ANY
  set ipv4 vpn next-hop 0.0.0.0
  on-match next
exit
route-map rm_cluster_import permit 2
  set local-preference 50
  on-match next
exit
!
route-map rm_cluster_import deny 65533
  match ip address prefix-list DEFAULT
  match source-vrf p_zerotrust
exit
route-map rm_cluster_import deny 65534
  match ipv6 address prefix-list DEFAULT
  match source-vrf {{ $.Config.ManagementVRF.Name }}
exit
route-map rm_cluster_import permit 65535
  match source-vrf {{ $.Config.ManagementVRF.Name }}
exit
!
{{ if containsKey $.NodeConfig.FabricVRFs $.Config.ManagementVRF.Name }}
{{ $vrf := index $.NodeConfig.FabricVRFs $.Config.ManagementVRF.Name }}
{{ template "vrfFilters" dict "Vrf" $.Config.ManagementVRF.Name "Imports" $vrf.VRFImports "BGPPeers" $vrf.BGPPeers }}
{{ end }}
!
{{ if $.NodeConfig.ClusterVRF }}
{{ range $i, $pbrRule := $.NodeConfig.ClusterVRF.PolicyRoutes }}
pbr-map hbr seq {{ add $i 1 }}
{{ if $pbrRule.TrafficMatch.SrcPrefix }}match src-ip {{ $pbrRule.TrafficMatch.SrcPrefix }}{{ end }}
{{ if $pbrRule.TrafficMatch.DstPrefix }}match dst-ip {{ $pbrRule.TrafficMatch.DstPrefix }}{{ end }}
{{ if $pbrRule.TrafficMatch.SrcPort }}match src-port {{ $pbrRule.TrafficMatch.SrcPort }}{{ end }}
{{ if $pbrRule.TrafficMatch.DstPort }}match dst-port {{ $pbrRule.TrafficMatch.DstPort }}{{ end }}
{{ if $pbrRule.TrafficMatch.Protocol }}match ip-protocol {{ $pbrRule.TrafficMatch.Protocol }}{{ end }}
{{ if $pbrRule.NextHop.Address }}
set nexthop {{ $pbrRule.NextHop.Address }}
{{ else if $pbrRule.NextHop.Vrf }}
set nexthop nexthop-vrf {{ $pbrRule.NextHop.Vrf }}
{{ end }}
exit
{{ end }}
{{ end }}
!
interface hbn
  pbr-policy hbn
exit
!
route-map TAG-FABRIC-IN permit 10
  set community 65169:200 additive
  set local-preference 100
exit
!
bgp community-list standard cm-received-fabric permit 65169:200
!
route-map DENY-TAG-FABRIC-OUT deny 10
  match community cm-received-fabric
exit
!
route-map DENY-TAG-FABRIC-OUT permit 20
exit
!
route-map rm_export_local deny 10
  match community cm-received-fabric
exit
!
route-map rm_export_local deny 11
  match ip address prefix-list pl_link_local
exit
!
route-map rm_export_local deny 12
  match ipv6 address prefix-list pl_link_local
exit
!
route-map rm_export_local permit 20
exit
!
bfd
exit
!