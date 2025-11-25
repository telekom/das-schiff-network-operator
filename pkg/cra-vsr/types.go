/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cra

import (
	"encoding/xml"
	"sort"

	"github.com/nemith/netconf"
)

type NoStringType string
type Policy string
type IO string
type IPvX int

const (
	NoString NoStringType = ""
	Permit   Policy       = "permit"
	Deny     Policy       = "deny"
	OUT      IO           = "out"
	IN       IO           = "in"
	IPv4     IPvX         = 4
	IPv6     IPvX         = 6

	DefaultAllowASIn           int = 3
	DefaultPrefixListSeqNum    int = 5
	DefaultCommunityListSeqNum int = 5
)

type VRouter struct {
	XMLName    xml.Name       `xml:"urn:6wind:vrouter config"`
	Namespaces []Namespace    `xml:"vrf,omitempty"`
	Routing    *GlobalRouting `xml:"routing,omitempty"`
}

type GlobalRouting struct {
	XMLName      xml.Name              `xml:"urn:6wind:vrouter/routing routing"`
	NCOperation  netconf.MergeStrategy `xml:"nc:operation,attr,omitempty"`
	RouteMaps    []RouteMap            `xml:"route-map,omitempty"`
	PrefixListV4 []PrefixList          `xml:"ipv4-prefix-list,omitempty"`
	PrefixListV6 []PrefixList          `xml:"ipv6-prefix-list,omitempty"`
	BGP          *GlobalBGP            `xml:"bgp,omitempty"`
}

type GlobalBGP struct {
	XMLName        xml.Name           `xml:"urn:6wind:vrouter/bgp bgp"`
	CommunityLists []BGPCommunityList `xml:"community-list,omitempty"`
}

type BGPCommunityList struct {
	Name string                `xml:"name"`
	Seqs []BGPCommunityListSeq `xml:"policy,omitempty"`
}

type BGPCommunityListSeq struct {
	Num    int      `xml:"priority"`
	Policy Policy   `xml:"policy"`
	Attrs  []string `xml:"community,omitempty"`
}

type PrefixList struct {
	Name string          `xml:"name"`
	Seqs []PrefixListSeq `xml:"seq,omitempty"`
}

type PrefixListSeq struct {
	Num     int     `xml:"num"`
	Policy  Policy  `xml:"policy"`
	Address *string `xml:"address,omitempty"`
	GE      *int    `xml:"ge,omitempty"`
	LE      *int    `xml:"le,omitempty"`
}

type RouteMap struct {
	Name string     `xml:"name"`
	Seqs []RtMapSeq `xml:"seq,omitempty"`
}

type RtMapSeq struct {
	Num     int         `xml:"num"`
	Policy  Policy      `xml:"policy"`
	Match   *RtMapMatch `xml:"match,omitempty"`
	Set     *RtMapSet   `xml:"set,omitempty"`
	OnMatch *string     `xml:"on-match,omitempty"`
	Call    *string     `xml:"call,omitempty"`
}

type RtMapMatch struct {
	Community *RtMapMatchCommunity `xml:"community,omitempty"`
	IPv4      *RtMapMatchIP        `xml:"ip>address,omitempty"`
	IPv6      *RtMapMatchIP        `xml:"ipv6>address,omitempty"`
	SourceVRF *string              `xml:"source-l3vrf,omitempty"`
}

type RtMapMatchCommunity struct {
	XMLName    xml.Name `xml:"urn:6wind:vrouter/bgp community"`
	ID         string   `xml:"id"`
	ExactMatch *bool    `xml:"exact-match,omitempty"`
}

type RtMapMatchIP struct {
	AccessList *string `xml:"access-list,omitempty"`
	PrefixList *string `xml:"prefix-list,omitempty"`
	PrefixLen  *int    `xml:"prefix-len,omitempty"`
}

type RtMapSet struct {
	IPv4            *RtMapSetIP        `xml:"ipv4,omitempty"`
	LocalPreference *int               `xml:"local-preference,omitempty"`
	Community       *RtMapSetCommunity `xml:"community,omitempty"`
	CommListDelete  *string            `xml:"comm-list-delete,omitempty"`
}

type RtMapSetIP struct {
	NextHop *string `xml:"vpn>next-hop,omitempty"`
}

type RtMapSetCommunity struct {
	XMLName xml.Name             `xml:"urn:6wind:vrouter/bgp community"`
	None    *NoStringType        `xml:"next-hop,omitempty"`
	Replace *RtMapSetCommReplace `xml:"replace-by,omitempty"`
	Add     *RtMapSetCommAdd     `xml:"add,omitempty"`
}

type RtMapSetCommReplace struct {
	Attrs []string `xml:"attribute,omitempty"`
}

type RtMapSetCommAdd struct {
	Attrs []string `xml:"attribute,omitempty"`
}

type Namespace struct {
	XMLName    xml.Name    `xml:"vrf"`
	Name       string      `xml:"name"`
	Routing    *Routing    `xml:"routing,omitempty"`
	Interfaces *Interfaces `xml:"interface,omitempty"`
	VRFs       []VRF       `xml:"l3vrf,omitempty"`
}

type VRF struct {
	Name       string      `xml:"name"`
	TableID    int         `xml:"table-id,omitempty"`
	Routing    *Routing    `xml:"routing,omitempty"`
	Interfaces *Interfaces `xml:"interface,omitempty"`
}

type Routing struct {
	XMLName     xml.Name              `xml:"urn:6wind:vrouter/routing routing"`
	NCOperation netconf.MergeStrategy `xml:"nc:operation,attr,omitempty"`
	Static      *StaticRouting        `xml:"static,omitempty"`
	PBR         *PolicyBasedRouting   `xml:"policy-based-routing,omitempty"`
	BGP         *BGP                  `xml:"bgp,omitempty"`
	*RoutingState
}

type RoutingState struct {
	EVPN EVPN `xml:"evpn,omitempty"`
}

type EVPN struct {
	VNIs []VniEVPN `xml:"vni"`
}

type VniEVPN struct {
	VNI   int    `xml:"vni"`
	Type  string `xml:"type"`
	VXLAN string `xml:"vxlan"`
	SVI   string `xml:"svi"`
	State string `xml:"state"`
}

type StaticRouting struct {
	IPv4 []StaticRoute `xml:"ipv4-route,omitempty"`
	IPv6 []StaticRoute `xml:"ipv6-route,omitempty"`
}

type StaticRoute struct {
	Destination string    `xml:"destination"`
	NextHops    []NextHop `xml:"next-hop"`
}

type NextHop struct {
	NextHop string  `xml:"next-hop"`
	VRF     *string `xml:"nexthop-l3vrf,omitempty"`
}

type PolicyBasedRouting struct {
	XMLName xml.Name `xml:"urn:6wind:vrouter/pbr policy-based-routing"`
	IPv4    []Rule   `xml:"ipv4-rule,omitempty"`
	IPv6    []Rule   `xml:"ipv6-rule,omitempty"`
}

type Rule struct {
	Priority int           `xml:"priority"`
	Match    *RuleMatch    `xml:"match,omitempty"`
	Action   *RuleAction   `xml:"action,omitempty"`
	Not      *NoStringType `xml:"not,omitempty"`
}

type RuleMatch struct {
	Interface     *string `xml:"inbound-interface,omitempty"`
	SourceIP      *string `xml:"source,omitempty"`
	DestinationIP *string `xml:"destination,omitempty"`
}

type RuleAction struct {
	Lookup string `xml:"lookup"`
}

type IPAddressList struct {
	IPAddresses []IPAddress `xml:"address,omitempty"`
}

type IPAddress struct {
	IP   string  `xml:"ip"`
	Peer *string `xml:"peer,omitempty"`
}

type Ethernet struct {
	MacAddress string `xml:"mac-address"`
}

type NetworkStack struct {
	IPv4     *NetworkStackV4       `xml:"ipv4,omitempty"`
	IPv6     *NetworkStackV6       `xml:"ipv6,omitempty"`
	Neighbor *NeighborNetworkStack `xml:"neighbor,omitempty"`
}

type NetworkStackV4 struct {
	AcceptARP *string `xml:"arp-accept-gratuitous,omitempty"`
}

type AddressGenMode string

const (
	NoLinkLocal AddressGenMode = "no-link-local"
	EUI64       AddressGenMode = "eui64"
)

type NetworkStackV6 struct {
	AddrGenMode       *AddressGenMode `xml:"address-generation-mode,omitempty"`
	AcceptDuplicateAD *string         `xml:"accept-duplicate-address-detection,omitempty"`
	AcceptUntrackedNA *string         `xml:"accept-untracked-neighbor-advertisement"`
}

type NeighborNetworkStack struct {
	BaseReachableTimeV4 *int `xml:"ipv4-base-reachable-time,omitempty"`
	BaseReachableTimeV6 *int `xml:"ipv6-base-reachable-time,omitempty"`
}

type Interfaces struct {
	XMLName   xml.Name         `xml:"urn:6wind:vrouter/interface interface"`
	Physicals []Physical       `xml:"physical,omitempty"`
	Bridges   []Bridge         `xml:"bridge,omitempty"`
	VXLANs    []VXLAN          `xml:"vxlan,omitempty"`
	VLANs     []VLAN           `xml:"vlan,omitempty"`
	Infras    []Infrastructure `xml:"infrastructure,omitempty"`
}

type Infrastructure struct {
	Name string `xml:"name"`
}

type Physical struct {
	Name         string         `xml:"name"`
	Port         string         `xml:"port"`
	IPv4         *IPAddressList `xml:"ipv4,omitempty"`
	IPv6         *IPAddressList `xml:"ipv6,omitempty"`
	NetworkStack *NetworkStack  `xml:"network-stack,omitempty"`
}

type Bridge struct {
	XMLName      xml.Name       `xml:"urn:6wind:vrouter/bridge bridge"`
	Name         string         `xml:"name"`
	MTU          *int           `xml:"mtu,omitempty"`
	Ethernet     *Ethernet      `xml:"ethernet,omitempty"`
	Slaves       []BridgeSlave  `xml:"link-interface,omitempty"`
	IPv4         *IPAddressList `xml:"ipv4,omitempty"`
	IPv6         *IPAddressList `xml:"ipv6,omitempty"`
	NetworkStack *NetworkStack  `xml:"network-stack,omitempty"`
}

type BridgeSlave struct {
	Name             string `xml:"slave"`
	Learning         *bool  `xml:"learning,omitempty"`
	NeighborSuppress *bool  `xml:"neighbor-suppress,omitempty"`
	Hairpin          *bool  `xml:"hairpin,omitempty"`
}

type VXLAN struct {
	XMLName       xml.Name       `xml:"urn:6wind:vrouter/vxlan vxlan"`
	Name          string         `xml:"name"`
	VNI           int            `xml:"vni"`
	MTU           *int           `xml:"mtu,omitempty"`
	Port          *int           `xml:"dst,omitempty"`
	Local         *string        `xml:"local,omitempty"`
	Learning      *bool          `xml:"learning,omitempty"`
	Ethernet      *Ethernet      `xml:"ethernet,omitempty"`
	IPv4          *IPAddressList `xml:"ipv4,omitempty"`
	IPv6          *IPAddressList `xml:"ipv6,omitempty"`
	NetworkStack  *NetworkStack  `xml:"network-stack,omitempty"`
	LinkInterface *string        `xml:"link-interface,omitempty"`
}

type VLAN struct {
	XMLName       xml.Name      `xml:"urn:6wind:vrouter/vlan vlan"`
	Name          string        `xml:"name"`
	VlanID        int           `xml:"vlan-id"`
	LinkInterface string        `xml:"link-interface"`
	MTU           *int          `xml:"mtu,omitempty"`
	NetworkStack  *NetworkStack `xml:"network-stack,omitempty"`
}

type BGP struct {
	XMLName            xml.Name           `xml:"urn:6wind:vrouter/bgp bgp"`
	AS                 string             `xml:"as"`
	RouterID           *string            `xml:"router-id,omitempty"`
	SuppressDuplicates *bool              `xml:"suppress-duplicates,omitempty"`
	EBGPNeedPolicy     *bool              `xml:"ebgp-requires-policy,omitempty"`
	VNI                *int               `xml:"l3vni,omitempty"`
	Listen             *BGPListen         `xml:"listen,omitempty"`
	AF                 *BGPAddrFamily     `xml:"address-family,omitempty"`
	Bestpath           *BGPBestpath       `xml:"bestpath,omitempty"`
	NeighGroups        []BGPNeighborGroup `xml:"neighbor-group,omitempty"`
	NeighborIPs        []BGPNeighborIP    `xml:"neighbor,omitempty"`
	NeighborIFs        []BGPNeighborIF    `xml:"unnumbered-neighbor,omitempty"`
}

type BGPListen struct {
	Limit  *int            `xml:"limit,omitempty"`
	Ranges []BGPNeighRange `xml:"neighbor-range,omitempty"`
}

type BGPNeighRange struct {
	Range string `xml:"address"`
	Group string `xml:"neighbor-group"`
}

type BGPBestpath struct {
	ASPath *BGPBestpathASPath `xml:"as-path,omitempty"`
}

type BGPMultipathRelax string

const (
	BGPMultipathRelaxSet   BGPMultipathRelax = "as-set"
	BGPMultipathRelaxNoSet BGPMultipathRelax = "no-as-set"
)

type BGPBestpathASPath struct {
	Confederation  *bool              `xml:"confederation,omitempty"`
	Ignore         *bool              `xml:"ignore,omitempty"`
	MultipathRelax *BGPMultipathRelax `xml:"multipath-relax,omitempty"`
}

type BGPAddrFamily struct {
	UcastV4 *BGPUcast    `xml:"ipv4-unicast,omitempty"`
	UcastV6 *BGPUcast    `xml:"ipv6-unicast,omitempty"`
	EVPN    *BGPEtherVPN `xml:"l2vpn-evpn,omitempty"`
}

type BGPRedistProto string

const (
	BGPRedistConnect BGPRedistProto = "connected"
	BGPRedistStatic  BGPRedistProto = "static"
	BGPRedistKernel  BGPRedistProto = "kernel"
)

type BGPUcast struct {
	Network    []BGPUcastNetwork `xml:"network,omitempty"`
	Redists    []BGPRedist       `xml:"redistribute,omitempty"`
	VRFImports *BGPUcastVRF      `xml:"l3vrf,omitempty"`
}

type BGPUcastNetwork struct {
	Prefix string `xml:"ip-prefix"`
}

type BGPUcastVRF struct {
	Imports *BGPUcastImportVRF `xml:"import,omitempty"`
}

type BGPUcastImportVRF struct {
	VRFs      []string `xml:"l3vrf,omitempty"`
	RouteMaps []string `xml:"route-map,omitempty"`
}

type BGPRedist struct {
	Protocol BGPRedistProto `xml:"protocol"`
	RouteMap *string        `xml:"route-map,omitempty"`
}

type BGPEtherVPN struct {
	AdvertAllVNI *bool          `xml:"advertise-all-vni,omitempty"`
	Advertise    *BGPAdvert     `xml:"advertisement,omitempty"`
	Exports      *BGPExportEVPN `xml:"export,omitempty"`
	Imports      *BGPImportEVPN `xml:"import,omitempty"`
	VNIs         []BGPVniEVPN   `xml:"vni,omitempty"`
}

type BGPVniEVPN struct {
	VNI     int            `xml:"vni"`
	Exports *BGPExportEVPN `xml:"export,omitempty"`
	Imports *BGPImportEVPN `xml:"import,omitempty"`
}

type BGPExportEVPN struct {
	RouteTargets []string `xml:"route-target,omitempty"`
	RouteDisting *string  `xml:"route-distinguisher,omitempty"`
}

type BGPImportEVPN struct {
	RouteTargets []string `xml:"route-target,omitempty"`
}

type BGPAdvert struct {
	UcastV4 *BGPAdvertUcast `xml:"ipv4-unicast,omitempty"`
	UcastV6 *BGPAdvertUcast `xml:"ipv6-unicast,omitempty"`
}

type BGPAdvertUcast struct {
	RouteMap *string `xml:"route-map,omitempty"`
}

type BGPNeighborGroup struct {
	Name string `xml:"name"`
	BGPNeighbor
}

type BGPNeighborIP struct {
	Address    string  `xml:"neighbor-address"`
	NeighGroup *string `xml:"neighbor-group,omitempty"`
	Interface  *string `xml:"interface,omitempty"`
	BGPNeighbor
}

type BGPNeighborIF struct {
	Interface  string  `xml:"interface"`
	NeighGroup *string `xml:"neighbor-group,omitempty"`
	IPv6Only   *bool   `xml:"ipv6-only,omitempty"`
	BGPNeighbor
}

type BGPNeighbor struct {
	EnforceFirstAS *bool            `xml:"enforce-first-as,omitempty"`
	RemoteAS       *string          `xml:"remote-as,omitempty"`
	LocalAS        *BGPNeighLocalAS `xml:"local-as,omitempty"`
	Timers         *BGPNeighTimers  `xml:"timers,omitempty"`
	AF             *BGPNeighAF      `xml:"address-family,omitempty"`
	UpdateSrc      *string          `xml:"update-source,omitempty"`
	EnforceMHops   *bool            `xml:"enforce-multihop,omitempty"`
	TTLSecHops     *int             `xml:"ttl-security-hops,omitempty"`
	Track          *string          `xml:"track,omitempty"`
	*BGPNeighborState
}

type BGPNeighborState struct {
	State             string           `xml:"state"`
	EstablishmentDate string           `xml:"established-date"`
	Statistics        BGPNeighborStats `xml:"message-statistics"`
}

type BGPNeighborStats struct {
	PacketWaitProcess int `xml:"packet-wait-process"`
	PacketWaitWritten int `xml:"packet-wait-written"`
	OpenSent          int `xml:"opent-sent"`
	OpenRecv          int `xml:"opens-received"`
	NotifSent         int `xml:"notifications-sent"`
	NotificationRecv  int `xml:"notifications-received"`
	UpdateSent        int `xml:"updates-sent"`
	UpdateRecv        int `xml:"updates-received"`
	KeepaliveSent     int `xml:"keepalives-sent"`
	KeepaliveRecv     int `xml:"keepalives-received"`
	RouteRefreshSent  int `xml:"route-refresh-sent"`
	RouteRefreshRecv  int `xml:"route-refresh-received"`
	CapabilitySent    int `xml:"capability-sent"`
	CapabilityRecv    int `xml:"capability-received"`
	TotalSent         int `xml:"total-sent"`
	TotalRecv         int `xml:"total-received"`
}

type BGPNeighLocalAS struct {
	Number    string `xml:"as-number"`
	NoPrepend *bool  `xml:"no-prepend,omitempty"`
	ReplaceAS *bool  `xml:"replace-as,omitempty"`
}

type BGPNeighTimers struct {
	AdvertInterval    *int `xml:"advertisement-interval,omitempty"`
	ConnectRetry      *int `xml:"connect-retry,omitempty"`
	KeepAliveInterval *int `xml:"keepalive-interval,omitempty"`
	HoldTime          *int `xml:"hold-time,omitempty"`
}

type BGPNeighAF struct {
	UcastV4 *BGPNeighUcast `xml:"ipv4-unicast,omitempty"`
	UcastV6 *BGPNeighUcast `xml:"ipv6-unicast,omitempty"`
	EVPN    *BGPNeighEVPN  `xml:"l2vpn-evpn,omitempty"`
}

type BGPNeighAFState struct {
	UpdateGroupID         int    `xml:"update-group-id"`
	SubGroupID            int    `xml:"sub-group-id"`
	PacketQueueLength     int    `xml:"packet-queue-length"`
	PrefixAccepted        int    `xml:"accepted-prefix"`
	PrefixSent            int    `xml:"sent-prefixes"`
	EbgpPolicyRequiredIn  string `xml:"inbound-ebgp-requires-policy"`
	EbgpPolicyRequiredOut string `xml:"outbound-ebgp-requires-policy"`
}

type BGPNeighUcast struct {
	AllowASIn   *int                 `xml:"allowas-in,omitempty"`
	RouteMaps   []BGPNeighRouteMap   `xml:"route-map,omitempty"`
	PrefixLists []BGPNeighPrefixList `xml:"prefix-list,omitempty"`
	MaxPrefix   *BGPNeighMaxPrefix   `xml:"maximum-prefix,omitempty"`
	*BGPNeighAFState
}

type BGPNeighMaxPrefix struct {
	Maximum   int   `xml:"maximum"`
	Threshold *int  `xml:"threshold,omitempty"`
	Restart   *int  `xml:"restart,omitempty"`
	WarnOnly  *bool `xml:"warning-only,omitempty"`
}

type BGPNeighEVPN struct {
	AllowASIn *int               `xml:"allowas-in,omitempty"`
	RouteMaps []BGPNeighRouteMap `xml:"route-map,omitempty"`
	*BGPNeighAFState
}

type BGPNeighRouteMap struct {
	Name      string `xml:"route-map-name"`
	Direction IO     `xml:"route-direction"`
}

type BGPNeighPrefixList struct {
	Name      string `xml:"prefix-list-name"`
	Direction IO     `xml:"update-direction"`
}

type ShowIPv4RouteSummaryInput struct {
	XMLName   xml.Name `xml:"urn:6wind:vrouter/routing show-ipv4-routes-summary"`
	Namespace *string  `xml:"vrf,omitempty"`
	VRF       *string  `xml:"l3vrf,omitempty"`
}

type ShowIPv6RouteSummaryInput struct {
	XMLName   xml.Name `xml:"urn:6wind:vrouter/routing show-ipv6-routes-summary"`
	Namespace *string  `xml:"vrf,omitempty"`
	VRF       *string  `xml:"l3vrf,omitempty"`
}

type ShowRouteSummaryOutput struct {
	Total  ShowRouteSummaryTotal      `xml:"total"`
	Routes []ShowRouteSummaryProtocol `xml:"route"`
}

type ShowRouteSummaryTotal struct {
	RIB int `xml:"routes-in-rib"`
	FIB int `xml:"routes-in-fib"`
}

type ShowRouteSummaryProtocol struct {
	Protocol string `xml:"protocol"`
	RIB      int    `xml:"routes-in-rib"`
	FIB      int    `xml:"routes-in-fib"`
}

type ShowNeighborsInput struct {
	XMLName   xml.Name `xml:"urn:6wind:vrouter/system show-neighbors"`
	Namespace *string  `xml:"vrf,omitempty"`
	Family    *string  `xml:"family,omitempty"`
	Interface *string  `xml:"interface,omitempty"`
}

type ShowNeighborsOutput struct {
	Neighbors []ShowNeighborEntry `xml:"neighbor,omitempty"`
}

type ShowNeighborEntry struct {
	LinkLayerAddress string `xml:"link-layer-address"`
	IPAddress        string `xml:"neighbor"`
	Interface        string `xml:"interface"`
	State            string `xml:"state"`
	Origin           string `xml:"origin"`
}

type ShowBridgeFDBInput struct {
	XMLName   xml.Name `xml:"urn:6wind:vrouter/bridge show-bridge-fdb"`
	Namespace *string  `xml:"vrf,omitempty"`
	Interface *string  `xml:"name,omitempty"`
}

type ShowBridgeFDBOutput struct {
	Bridges []ShowBridgeFDBEntry `xml:"bridge,omitempty"`
}

type ShowBridgeFDBEntry struct {
	Name      string                    `xml:"name"`
	Neighbors []ShowBridgeFDBNeighEntry `xml:"fdb"`
}

type ShowBridgeFDBNeighEntry struct {
	LinkLayerAddress string `xml:"link-layer-address"`
	LinkInterface    string `xml:"link-interface"`
	State            string `xml:"state"`
	Origin           string `xml:"origin"`
}

func lookupNS(vrouter *VRouter, name string) *Namespace {
	for i := range vrouter.Namespaces {
		if vrouter.Namespaces[i].Name == name {
			return &vrouter.Namespaces[i]
		}
	}
	return nil
}

func lookupVRF(ns *Namespace, name string) *VRF {
	for i := range ns.VRFs {
		if ns.VRFs[i].Name == name {
			return &ns.VRFs[i]
		}
	}
	return nil
}

func (ip *IPAddressList) Sort() {
	sort.Slice(ip.IPAddresses, func(i, j int) bool {
		return ip.IPAddresses[i].IP < ip.IPAddresses[j].IP
	})
}

func (phys *Physical) Sort() {
	if phys.IPv4 != nil {
		phys.IPv4.Sort()
	}
	if phys.IPv6 != nil {
		phys.IPv4.Sort()
	}
}

func (br *Bridge) Sort() {
	sort.Slice(br.Slaves, func(i, j int) bool {
		return br.Slaves[i].Name < br.Slaves[j].Name
	})
	if br.IPv4 != nil {
		br.IPv4.Sort()
	}
	if br.IPv6 != nil {
		br.IPv4.Sort()
	}
}

func (vxlan *VXLAN) Sort() {
	if vxlan.IPv4 != nil {
		vxlan.IPv4.Sort()
	}
	if vxlan.IPv6 != nil {
		vxlan.IPv4.Sort()
	}
}

func (intfs *Interfaces) Sort() {
	sort.Slice(intfs.Physicals, func(i, j int) bool {
		return intfs.Physicals[i].Name < intfs.Physicals[j].Name
	})
	sort.Slice(intfs.Bridges, func(i, j int) bool {
		return intfs.Bridges[i].Name < intfs.Bridges[j].Name
	})
	sort.Slice(intfs.VXLANs, func(i, j int) bool {
		return intfs.VXLANs[i].Name < intfs.VXLANs[j].Name
	})
	sort.Slice(intfs.VLANs, func(i, j int) bool {
		return intfs.VLANs[i].Name < intfs.VLANs[j].Name
	})
	sort.Slice(intfs.Infras, func(i, j int) bool {
		return intfs.Infras[i].Name < intfs.Infras[j].Name
	})

	for _, phys := range intfs.Physicals {
		phys.Sort()
	}
	for _, br := range intfs.Bridges {
		br.Sort()
	}
	for i := range intfs.VXLANs {
		intfs.VXLANs[i].Sort()
	}
}

func (route *StaticRoute) Sort() {
	sort.Slice(route.NextHops, func(i, j int) bool {
		return route.NextHops[i].NextHop < route.NextHops[j].NextHop
	})
}

func (routes *StaticRouting) Sort() {
	sort.Slice(routes.IPv4, func(i, j int) bool {
		return routes.IPv4[i].Destination < routes.IPv4[j].Destination
	})
	sort.Slice(routes.IPv6, func(i, j int) bool {
		return routes.IPv6[i].Destination < routes.IPv6[j].Destination
	})
	for _, route := range routes.IPv4 {
		route.Sort()
	}
	for _, route := range routes.IPv6 {
		route.Sort()
	}
}

func (ucast *BGPNeighUcast) Sort() {
	sort.Slice(ucast.RouteMaps, func(i, j int) bool {
		return ucast.RouteMaps[i].Name < ucast.RouteMaps[j].Name
	})
	sort.Slice(ucast.PrefixLists, func(i, j int) bool {
		return ucast.PrefixLists[i].Name < ucast.PrefixLists[j].Name
	})
}

func (evpn *BGPNeighEVPN) Sort() {
	sort.Slice(evpn.RouteMaps, func(i, j int) bool {
		return evpn.RouteMaps[i].Name < evpn.RouteMaps[j].Name
	})
}

func (neigh *BGPNeighbor) Sort() {
	if neigh.AF != nil {
		if neigh.AF.UcastV4 != nil {
			neigh.AF.UcastV4.Sort()
		}
		if neigh.AF.UcastV6 != nil {
			neigh.AF.UcastV6.Sort()
		}
		if neigh.AF.EVPN != nil {
			neigh.AF.EVPN.Sort()
		}
	}
}

func (ucast *BGPUcast) Sort() {
	sort.Slice(ucast.Redists, func(i, j int) bool {
		return ucast.Redists[i].Protocol < ucast.Redists[j].Protocol
	})

	if ucast.VRFImports != nil && ucast.VRFImports.Imports != nil {
		imports := ucast.VRFImports.Imports
		sort.Slice(imports.VRFs, func(i, j int) bool {
			return imports.VRFs[i] < imports.VRFs[j]
		})
		sort.Slice(imports.RouteMaps, func(i, j int) bool {
			return imports.RouteMaps[i] < imports.RouteMaps[j]
		})
	}
}

func (evpn *BGPEtherVPN) Sort() {
	sort.Slice(evpn.VNIs, func(i, j int) bool {
		return evpn.VNIs[i].VNI < evpn.VNIs[j].VNI
	})

	for _, vni := range evpn.VNIs {
		sort.Slice(vni.Exports.RouteTargets, func(i, j int) bool {
			return vni.Exports.RouteTargets[i] < vni.Exports.RouteTargets[j]
		})
		sort.Slice(vni.Imports.RouteTargets, func(i, j int) bool {
			return vni.Imports.RouteTargets[i] < vni.Imports.RouteTargets[j]
		})
	}

	if evpn.Exports != nil {
		sort.Slice(evpn.Exports.RouteTargets, func(i, j int) bool {
			return evpn.Exports.RouteTargets[i] < evpn.Exports.RouteTargets[j]
		})
	}

	if evpn.Imports != nil {
		sort.Slice(evpn.Imports.RouteTargets, func(i, j int) bool {
			return evpn.Imports.RouteTargets[i] < evpn.Imports.RouteTargets[j]
		})
	}
}

func (bgp *BGP) Sort() {
	sort.Slice(bgp.NeighGroups, func(i, j int) bool {
		return bgp.NeighGroups[i].Name < bgp.NeighGroups[j].Name
	})
	sort.Slice(bgp.NeighborIPs, func(i, j int) bool {
		return bgp.NeighborIPs[i].Address < bgp.NeighborIPs[j].Address
	})
	sort.Slice(bgp.NeighborIFs, func(i, j int) bool {
		return bgp.NeighborIFs[i].Interface < bgp.NeighborIFs[j].Interface
	})

	for _, neigh := range bgp.NeighGroups {
		neigh.BGPNeighbor.Sort()
	}
	for _, neigh := range bgp.NeighborIPs {
		neigh.BGPNeighbor.Sort()
	}

	if bgp.AF != nil {
		if bgp.AF.UcastV4 != nil {
			bgp.AF.UcastV4.Sort()
		}
		if bgp.AF.UcastV6 != nil {
			bgp.AF.UcastV6.Sort()
		}
		if bgp.AF.EVPN != nil {
			bgp.AF.EVPN.Sort()
		}
	}

	if bgp.Listen != nil {
		sort.Slice(bgp.Listen.Ranges, func(i, j int) bool {
			return bgp.Listen.Ranges[i].Range < bgp.Listen.Ranges[j].Range
		})
	}
}

func (pbr *PolicyBasedRouting) Sort() {
	sort.Slice(pbr.IPv4, func(i, j int) bool {
		return pbr.IPv4[i].Priority < pbr.IPv4[j].Priority
	})
	sort.Slice(pbr.IPv6, func(i, j int) bool {
		return pbr.IPv6[i].Priority < pbr.IPv6[j].Priority
	})
}

func (routing *Routing) Sort() {
	if routing.Static != nil {
		routing.Static.Sort()
	}
	if routing.BGP != nil {
		routing.BGP.Sort()
	}
	if routing.PBR != nil {
		routing.PBR.Sort()
	}
}

func (vrf *VRF) Sort() {
	if vrf.Interfaces != nil {
		vrf.Interfaces.Sort()
	}
	if vrf.Routing != nil {
		vrf.Routing.Sort()
	}
}

func (ns *Namespace) Sort() {
	sort.Slice(ns.VRFs, func(i, j int) bool {
		return ns.VRFs[i].Name < ns.VRFs[j].Name
	})
	for _, vrf := range ns.VRFs {
		vrf.Sort()
	}
	if ns.Routing != nil {
		ns.Routing.Sort()
	}
	if ns.Interfaces != nil {
		ns.Interfaces.Sort()
	}
}

func (rtmap *RouteMap) Sort() {
	sort.Slice(rtmap.Seqs, func(i, j int) bool {
		return rtmap.Seqs[i].Num < rtmap.Seqs[j].Num
	})

	for _, seq := range rtmap.Seqs {
		if seq.Set != nil && seq.Set.Community != nil {
			comm := seq.Set.Community
			if comm.Replace != nil {
				sort.Slice(comm.Replace.Attrs, func(i, j int) bool {
					return comm.Replace.Attrs[i] < comm.Replace.Attrs[j]
				})
			}
			if comm.Add != nil {
				sort.Slice(comm.Add.Attrs, func(i, j int) bool {
					return comm.Add.Attrs[i] < comm.Add.Attrs[j]
				})
			}
		}
	}
}

func (pl *PrefixList) Sort() {
	sort.Slice(pl.Seqs, func(i, j int) bool {
		return pl.Seqs[i].Num < pl.Seqs[j].Num
	})
}

func (comml *BGPCommunityList) Sort() {
	sort.Slice(comml.Seqs, func(i, j int) bool {
		return comml.Seqs[i].Num < comml.Seqs[j].Num
	})

	for _, seq := range comml.Seqs {
		sort.Slice(seq.Attrs, func(i, j int) bool {
			return seq.Attrs[i] < seq.Attrs[j]
		})
	}
}

func (bgp *GlobalBGP) Sort() {
	sort.Slice(bgp.CommunityLists, func(i, j int) bool {
		return bgp.CommunityLists[i].Name < bgp.CommunityLists[j].Name
	})

	for _, comml := range bgp.CommunityLists {
		comml.Sort()
	}
}

func (rting *GlobalRouting) Sort() {
	sort.Slice(rting.RouteMaps, func(i, j int) bool {
		return rting.RouteMaps[i].Name < rting.RouteMaps[j].Name
	})
	sort.Slice(rting.PrefixListV4, func(i, j int) bool {
		return rting.PrefixListV4[i].Name < rting.PrefixListV4[j].Name
	})
	sort.Slice(rting.PrefixListV6, func(i, j int) bool {
		return rting.PrefixListV6[i].Name < rting.PrefixListV6[j].Name
	})

	for _, rtmap := range rting.RouteMaps {
		rtmap.Sort()
	}
	for _, pl := range rting.PrefixListV4 {
		pl.Sort()
	}
	for _, pl := range rting.PrefixListV6 {
		pl.Sort()
	}

	if rting.BGP != nil {
		rting.BGP.Sort()
	}
}

func (vr *VRouter) Sort() {
	if vr.Routing != nil {
		vr.Routing.Sort()
	}
	for _, ns := range vr.Namespaces {
		ns.Sort()
	}
}
