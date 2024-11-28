package southbound

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net"
)

// HBRConfig represents the configuration for HBR.
type HBRConfig struct {
	// Layer2s is a list of Layer2 configurations.
	Layer2s []Layer2 `json:"layer2s"`

	// DefaultVRF is the default VRF configuration used for the default route of HBR.
	DefaultVRF FabricVRF `json:"defaultVRF"`
	// FabricVRFs is a list of fabric VRF configurations.
	FabricVRFs []FabricVRF `json:"fabricVRFs"`
	// LocalVRFs is a list of local VRF configurations.
	LocalVRFs []VRF `json:"localVRFs"`
}

// Layer2 represents a Layer 2 network configuration.
type Layer2 struct {
	// Name is the name of the Layer 2 network.
	Name string `json:"name"`
	// VNI is the Virtual Network Identifier.
	VNI uint32 `json:"vni"`
	// VLAN is the VLAN ID.
	VLAN uint16 `json:"vlan"`
	// RouteTarget is the route target for the Layer 2 network.
	RouteTarget string `json:"routeTarget"`
	// MTU is the Maximum Transmission Unit size.
	MTU uint16 `json:"mtu"`
	// IRB is the Integrated Routing and Bridging configuration.
	IRB *IRB `json:"irb"`
}

// IRB represents the Integrated Routing and Bridging configuration.
type IRB struct {
	// VRF is the Virtual Routing and Forwarding instance.
	VRF string `json:"vrf"`
	// MACAddress is the MAC address for the IRB.
	MACAddress net.HardwareAddr `json:"macAddress"`
	// IPAddresses is a list of IP addresses for the IRB.
	IPAddresses []string `json:"ipAddresses"`
}

// VRF represents a Virtual Routing and Forwarding instance.
type VRF struct {
	// Name is the name of the VRF.
	Name string `json:"name"`
	// Loopbacks is a list of loopback interfaces.
	Loopbacks []Loopback `json:"loopbacks"`
	// BGPPeers is a list of BGP peers.
	BGPPeers []BGPPeer `json:"bgpPeers"`
	// VRFImports is a list of VRF import configurations.
	VRFImports []VRFImport `json:"vrfImports"`
	// StaticRoutes is a list of static routes.
	StaticRoutes []StaticRoute `json:"staticRoutes"`
	// PolicyRoutes is a list of policy-based routes.
	PolicyRoutes []PolicyRoute `json:"policyRoutes"`
	// MirrorACLs is a list of mirror ACLs.
	MirrorACLs []MirrorACL `json:"mirrorAcls"`
}

// FabricVRF represents a fabric VRF configuration.
type FabricVRF struct {
	VRF
	// VNI is the Virtual Network Identifier.
	VNI *uint32 `json:"vni"`
	// EVPNImportRouteTargets is a list of EVPN import route targets.
	EVPNImportRouteTargets []string `json:"evpnImportRouteTargets"`
	// EVPNExportRouteTargets is a list of EVPN export route targets.
	EVPNExportRouteTargets []string `json:"evpnExportRouteTargets"`
	// EVPNExportFilter is the export filter for EVPN.
	EVPNExportFilter *Filter `json:"evpnExportFilter"`
}

// Loopback represents a loopback interface.
type Loopback struct {
	// Name is the name of the loopback interface.
	Name string `json:"name"`
	// IPAddresses is a list of IP addresses for the loopback interface.
	IPAddresses []string `json:"ipAddresses"`
}

// VRFImport represents a VRF import configuration.
type VRFImport struct {
	// FromVRF is the source VRF for the import.
	FromVRF string `json:"fromVrf"`
	// Filter is the filter applied to the import.
	Filter Filter `json:"filter"`
}

// BGPPeer represents a BGP peer configuration.
type BGPPeer struct {
	// Address is the address of the BGP peer.
	Address *string `json:"address"`
	// ListenRange is the listen range for the BGP peer.
	ListenRange *string `json:"listenRange"`
	// RemoteASN is the remote Autonomous System Number.
	RemoteASN uint32 `json:"remoteAsn"`
	// IPv4 is the IPv4 address family configuration.
	IPv4 *AddressFamily `json:"ipv4"`
	// IPv6 is the IPv6 address family configuration.
	IPv6 *AddressFamily `json:"ipv6"`
	// BFDProfile is the BFD profile for the BGP peer.
	BFDProfile *BFDProfile `json:"bfdProfile"`
	// HoldTime is the hold time for the BGP session.
	HoldTime *metav1.Duration `json:"holdTime"`
	// KeepaliveTime is the keepalive time for the BGP session.
	KeepaliveTime *metav1.Duration `json:"keepaliveTime"`
}

// BFDProfile represents a BFD profile configuration.
type BFDProfile struct {
	// MinInterval is the minimum interval for BFD.
	MinInterval uint32 `json:"minInterval"`
}

// AddressFamily represents an address family configuration.
type AddressFamily struct {
	// ExportFilter is the export filter for the address family.
	ExportFilter *Filter `json:"exportFilter"`
	// ImportFilter is the import filter for the address family.
	ImportFilter *Filter `json:"importFilter"`
	// MaxPrefixes is the maximum number of prefixes for the address family.
	MaxPrefixes *uint32 `json:"maxPrefixes"`
}

// Filter represents a filter configuration.
type Filter struct {
	// Items is a list of filter items.
	Items []FilterItem `json:"items"`
	// DefaultAction is the default action for the filter.
	DefaultAction Action `json:"defaultAction"`
}

// FilterItem represents a filter item.
type FilterItem struct {
	// Matcher is the matcher for the filter item.
	Matcher Matcher `json:"matcher"`
	// Action is the action for the filter item.
	Action Action `json:"action"`
}

// Matcher represents a matcher configuration.
type Matcher struct {
	// Prefix is the prefix matcher.
	Prefix *PrefixMatcher `json:"prefix"`
	// BGPCommunity is the BGP community matcher.
	BGPCommunity *BGPCommunityMatcher `json:"bgpCommunity"`
}

// PrefixMatcher represents a prefix matcher.
type PrefixMatcher struct {
	// Prefix is the prefix to match.
	Prefix string `json:"prefix"`
	// Ge is the minimum prefix length to match.
	Ge *int `json:"ge"`
	// Le is the maximum prefix length to match.
	Le *int `json:"le"`
}

// BGPCommunityMatcher represents a BGP community matcher.
type BGPCommunityMatcher struct {
	// Community is the BGP community to match.
	Community string `json:"community"`
}

// Action represents an action configuration.
type Action struct {
	// Type is the type of action.
	Type ActionType `json:"type"`
	// ModifyRoute is the modify route action.
	ModifyRoute *ModifyRouteAction `json:"modifyRoute"`
}

// ActionType represents the type of action.
type ActionType string

const (
	// Accept represents an accept action.
	Accept ActionType = "accept"
	// Reject represents a reject action.
	Reject ActionType = "reject"
	// Next represents a next action.
	Next ActionType = "next"
)

// ModifyRouteAction represents a modify route action.
type ModifyRouteAction struct {
	// AddCommunity is the community to add to the route.
	AddCommunity string `json:"addCommunity"`
}

// StaticRoute represents a static route configuration.
type StaticRoute struct {
	// Prefix is the prefix for the static route.
	Prefix string `json:"prefix"`
	// NextHop is the next hop for the static route.
	NextHop NextHop `json:"nextHop"`
	// BFDProfile is the BFD profile for the static route.
	BFDProfile *BFDProfile `json:"bfdProfile"`
}

// TrafficMatch represents a traffic match configuration.
type TrafficMatch struct {
	// SrcPrefix is the source prefix to match.
	SrcPrefix *string `json:"srcPrefix"`
	// DstPrefix is the destination prefix to match.
	DstPrefix *string `json:"dstPrefix"`
	// SrcPort is the source port to match.
	SrcPort *uint16 `json:"srcPort"`
	// DstPort is the destination port to match.
	DstPort *uint16 `json:"dstPort"`
	// Protocol is the protocol to match.
	Protocol *string `json:"protocol"`
	// Layer2 is the Layer2 to match.
	Layer2 string `json:"layer2"`
}

// PolicyRoute represents a policy-based route configuration.
type PolicyRoute struct {
	// TrafficMatch is the traffic match for the policy route.
	TrafficMatch TrafficMatch `json:"trafficMatch"`
	// NextHop is the next hop for the policy route.
	NextHop NextHop `json:"nextHop"`
}

// MirrorACL represents a mirror ACL configuration.
type MirrorACL struct {
	// TrafficMatch is the traffic match for the mirror ACL.
	TrafficMatch TrafficMatch `json:"trafficMatch"`
	// DestinationAddress is the destination address for the mirrored traffic.
	DestinationAddress string `json:"destinationAddress"`
	// DestinationVrf is the destination VRF for the mirrored traffic.
	DestinationVrf string `json:"destinationVrf"`
}

// NextHop represents a next hop configuration.
type NextHop struct {
	// Address is the address of the next hop.
	Address *string `json:"address"`
	// Vrf is the VRF of the next hop.
	Vrf *string `json:"vrf"`
}
