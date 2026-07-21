/*
Copyright 2024.

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeNetworkConfigSpec defines the desired state of NodeConfig.
type NodeNetworkConfigSpec struct {
	// Revision stores hash of the NodeConfigRevision that was used to create the NodeNetworkConfig object.
	Revision string `json:"revision"`
	// Layer2s is a map of Layer2 configurations.
	Layer2s map[string]Layer2 `json:"layer2s,omitempty"`
	// ClusterVRF is the default VRF configuration used for the default route of HBR.
	ClusterVRF *VRF `json:"clusterVRF,omitempty"`
	// FabricVRFs is a map of fabric VRF configurations.
	FabricVRFs map[string]FabricVRF `json:"fabricVRFs,omitempty"`
	// LocalVRFs is a map of local VRF configurations.
	LocalVRFs map[string]VRF `json:"localVRFs,omitempty"`
}

// Layer2 represents a Layer 2 network configuration.
type Layer2 struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16777215
	// VNI is the Virtual Network Identifier.
	VNI uint32 `json:"vni"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4096
	// VLAN is the VLAN ID.
	VLAN uint16 `json:"vlan"`
	// RouteTarget is the route target for the Layer 2 network.
	RouteTarget string `json:"routeTarget"`
	// +kubebuilder:validation:Minimum=1000
	// +kubebuilder:validation:Maximum=9000
	// +kubebuilder:validation:ExclusiveMaximum=false
	// MTU is the Maximum Transmission Unit size.
	MTU uint16 `json:"mtu"`
	// IRB is the Integrated Routing and Bridging configuration.
	IRB *IRB `json:"irb,omitempty"`
	// MirrorACLs is a list of mirror ACLs.
	MirrorACLs []MirrorACL `json:"mirrorAcls,omitempty"`
	// DisableSegmentation indicates whether to disable segmentation for the Layer 2 network.
	DisableSegmentation bool `json:"disableSegmentation,omitempty"`
	// AttachmentRef identifies the Layer2Attachment that produced this Layer2.
	// It lets routed-CNI L2 port attachments (see AttachedPorts) bind to the
	// correct L2 domain by reference rather than by VNI. Empty for Layer2s built
	// from the legacy Layer2NetworkConfiguration path.
	// +optional
	AttachmentRef *Layer2AttachmentRef `json:"attachmentRef,omitempty"`
	// AttachedPorts are routed-CNI ports enslaved to this L2 bridge (moved into
	// the CRA netns and added as bridge link-interfaces). They are rendered as
	// bridge slaves (VSR link-interface, FRR master) with no L3 addressing.
	// +optional
	AttachedPorts []AttachedPort `json:"attachedPorts,omitempty"`
}

// Layer2AttachmentRef identifies a Layer2Attachment by namespaced name.
type Layer2AttachmentRef struct {
	// Name is the Layer2Attachment name.
	Name string `json:"name"`
	// Namespace is the Layer2Attachment namespace.
	Namespace string `json:"namespace"`
}

// PortTransport selects how an attached CRA-side port is wired.
// +kubebuilder:validation:Enum=veth;vhostuser
type PortTransport string

const (
	// PortTransportVeth is the default transport: a veth pair whose CRA-side end
	// is moved into the CRA network namespace and referenced by VSR as
	// infra-<ifname>. Supported by both the FRR and VSR flavors.
	PortTransportVeth PortTransport = "veth"
	// PortTransportVhostUser is a DPDK/virtio-user vhost-user socket, rendered by
	// VSR as an fpvhost fast-path virtual-port. VSR-only; unsupported on FRR.
	PortTransportVhostUser PortTransport = "vhostuser"
)

// PortWiring describes how a CRA-side attached port is wired. It is shared by
// routed ports (RoutedPort) and L2-attached ports (AttachedPort).
type PortWiring struct {
	// Transport selects the CRA-side wiring: "veth" (default, an infrastructure
	// port) or "vhostuser" (a VSR fpvhost fast-path virtual-port, VSR-only).
	// +optional
	// +kubebuilder:default=veth
	Transport PortTransport `json:"transport,omitempty"`
	// SocketPath is the vhost-user unix socket path shared with the workload.
	// Only meaningful when Transport is "vhostuser".
	// +optional
	SocketPath string `json:"socketPath,omitempty"`
	// SocketMode is the vhost-user socket mode ("client" or "server") from the
	// VSR fast-path perspective (already inverted from the workload's view).
	// Only meaningful when Transport is "vhostuser".
	// +optional
	SocketMode string `json:"socketMode,omitempty"`
}

// AttachedPort is a routed-CNI port bound to a Layer2 bridge (L2 attach mode).
// It carries no L3 addressing: the port is added as a bridge slave only.
type AttachedPort struct {
	// Interface is the interface name inside the CRA network namespace (the moved
	// veth end for veth transport, or the fpvhost interface for vhostuser).
	Interface string `json:"interface"`
	// PortWiring selects the CRA-side transport (veth default, or vhostuser).
	PortWiring `json:",inline"`
}

// IRB represents the Integrated Routing and Bridging configuration.
type IRB struct {
	// VRF is the Virtual Routing and Forwarding instance.
	VRF string `json:"vrf"`
	// +kubebuilder:validation:Pattern=`(?:[[:xdigit:]]{2}:){5}[[:xdigit:]]{2}`
	// MACAddress is the MAC address for the IRB.
	MACAddress string `json:"macAddress"`
	// IPAddresses is a list of IP addresses for the IRB.
	// +kubebuilder:validation:MinItems=1
	IPAddresses []string `json:"ipAddresses"`
}

// VRF represents a Virtual Routing and Forwarding instance.
type VRF struct {
	// Loopbacks is a list of loopback interfaces.
	Loopbacks map[string]Loopback `json:"loopbacks,omitempty"`
	// BGPPeers is a list of BGP peers.
	BGPPeers []BGPPeer `json:"bgpPeers,omitempty"`
	// VRFImports is a list of VRF import configurations.
	VRFImports []VRFImport `json:"vrfImports,omitempty"`
	// StaticRoutes is a list of static routes.
	StaticRoutes []StaticRoute `json:"staticRoutes,omitempty"`
	// PolicyRoutes is a list of policy-based routes.
	PolicyRoutes []PolicyRoute `json:"policyRoutes,omitempty"`
	// MirrorACLs is a list of mirror ACLs.
	MirrorACLs []MirrorACL `json:"mirrorAcls,omitempty"`
	// Redistribute is a config for BGP redistribution.
	Redistribute *Redistribute `json:"redistribute,omitempty"`
	// GREs is a map of GRE tunnel interfaces
	GREs map[string]GRE `json:"gres,omitempty"`
	// RoutedPorts is a list of routed CNI attachments (interfaces moved into the
	// CRA network namespace) bound into this VRF. The VSR flavor renders these as
	// infrastructure interfaces plus interface-static routes via NETCONF, because
	// the fast path owns the FIB and netlink cannot be used to program it.
	RoutedPorts []RoutedPort `json:"routedPorts,omitempty"`
}

// RoutedPort describes a routed workload attachment whose CRA-side interface was
// moved into the CRA network namespace by the routed CNI. On the VSR flavor the
// on-link gateway addresses and the workload host routes are pushed via NETCONF.
type RoutedPort struct {
	// Interface is the interface name inside the CRA network namespace (the moved
	// veth end, e.g. "cra0123456789ab"). VSR references it as infra-<interface>.
	Interface string `json:"interface"`
	// PortWiring selects the CRA-side transport (veth default, or vhostuser for
	// the VSR fpvhost fast-path virtual-port).
	PortWiring `json:",inline"`
	// GatewayV4 is the on-link IPv4 gateway address (with prefix length, e.g.
	// "169.254.100.100/32") configured on the infrastructure interface.
	GatewayV4 string `json:"gatewayV4,omitempty"`
	// GatewayV6 is the on-link IPv6 gateway address (with prefix length, e.g.
	// "fd00:7:caa5:1::/128") configured on the infrastructure interface.
	GatewayV6 string `json:"gatewayV6,omitempty"`
	// HostRoutes are the workload host addresses (e.g. "10.0.0.5/32",
	// "fd00:200::5/128") installed as interface-static routes via Interface so
	// VSR redistributes them into BGP.
	HostRoutes []string `json:"hostRoutes,omitempty"`
}

// Redistribute represents a BGP redistribution configuration.
type Redistribute struct {
	// Connected indicates whether to redistribute connected routes.
	Connected *Filter `json:"connected,omitempty"`
	// Static indicates whether to redistribute static routes.
	Static *Filter `json:"static,omitempty"`
}

// FabricVRF represents a fabric VRF configuration.
type FabricVRF struct {
	VRF `json:",inline"`
	// VNI is the Virtual Network Identifier.
	VNI uint32 `json:"vni"`
	// EVPNImportRouteTargets is a list of EVPN import route targets.
	EVPNImportRouteTargets []string `json:"evpnImportRouteTargets"`
	// EVPNExportRouteTargets is a list of EVPN export route targets.
	EVPNExportRouteTargets []string `json:"evpnExportRouteTargets"`
	// EVPNExportFilter is the export filter for EVPN.
	EVPNExportFilter *Filter `json:"evpnExportFilter"`
}

// Loopback represents a loopback interface.
type Loopback struct {
	// IPAddresses is a list of IP addresses for the loopback interface.
	// +kubebuilder:validation:MinItems=1
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
	Address *string `json:"address,omitempty"`
	// ListenRange is the listen range for the BGP peer.
	ListenRange *string `json:"listenRange,omitempty"`
	// RemoteASN is the remote Autonomous System Number.
	RemoteASN uint32 `json:"remoteAsn"`
	// IPv4 is the IPv4 address family configuration.
	IPv4 *AddressFamily `json:"ipv4,omitempty"`
	// IPv6 is the IPv6 address family configuration.
	IPv6 *AddressFamily `json:"ipv6,omitempty"`
	// BFDProfile is the BFD profile for the BGP peer.
	BFDProfile *BFDProfile `json:"bfdProfile,omitempty"`
	// Multihop is the flag to enable multihop for the BGP peer.
	Multihop *uint32 `json:"multihop,omitempty"`
	// HoldTime is the hold time for the BGP session, default is 90s.
	HoldTime *metav1.Duration `json:"holdTime,omitempty"`
	// KeepaliveTime is the keepalive time for the BGP session, default is 30s.
	KeepaliveTime *metav1.Duration `json:"keepaliveTime,omitempty"`
	// Password is an optional MD5/TCP-AO password for the BGP session.
	// Resolved on the controller side from the intent BGPPeering's AuthSecretRef
	// (key "password") and inlined here so node agents do not need Secret RBAC.
	// +optional
	Password *string `json:"password,omitempty"`
}

// BFDProfile represents a BFD profile configuration.
type BFDProfile struct {
	// MinInterval is the minimum interval for BFD.
	MinInterval uint32 `json:"minInterval"`
}

// AddressFamily represents an address family configuration.
type AddressFamily struct {
	// ExportFilter is the export filter for the address family.
	ExportFilter *Filter `json:"exportFilter,omitempty"`
	// ImportFilter is the import filter for the address family.
	ImportFilter *Filter `json:"importFilter,omitempty"`
	// MaxPrefixes is the maximum number of prefixes for the address family.
	MaxPrefixes *uint32 `json:"maxPrefixes,omitempty"`
}

// Filter represents a filter configuration.
type Filter struct {
	// Items is a list of filter items.
	Items []FilterItem `json:"items,omitempty"`
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
	Prefix *PrefixMatcher `json:"prefix,omitempty"`
	// BGPCommunity is the BGP community matcher.
	BGPCommunity *BGPCommunityMatcher `json:"bgpCommunity,omitempty"`
}

// PrefixMatcher represents a prefix matcher.
type PrefixMatcher struct {
	// Prefix is the prefix to match.
	Prefix string `json:"prefix"`
	// Ge is the minimum prefix length to match.
	Ge *int `json:"ge,omitempty"`
	// Le is the maximum prefix length to match.
	Le *int `json:"le,omitempty"`
}

// BGPCommunityMatcher represents a BGP community matcher.
type BGPCommunityMatcher struct {
	// Community is the BGP community to match.
	Community  string `json:"community"`
	ExactMatch bool   `json:"exactMatch"`
}

// Action represents an action configuration.
type Action struct {
	// Type is the type of action.
	// +kubebuilder:validation:Enum=accept;reject;next
	Type ActionType `json:"type"`
	// ModifyRoute is the modify route action.
	ModifyRoute *ModifyRouteAction `json:"modifyRoute,omitempty"`
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
	// AddCommunities is the community to add to the route.
	AddCommunities []string `json:"addCommunities,omitempty"`
	// AdditiveCommunities is the flag to add communities to the route, by default the communities are replaced.
	AdditiveCommunities *bool `json:"additiveCommunities,omitempty"`
	// RemoveCommunities is the community to remove from the route.
	RemoveCommunities []string `json:"removeCommunities,omitempty"`
	// RemoveAllCommunities is the flag to remove all communities from the route.
	RemoveAllCommunities *bool `json:"removeAllCommunities,omitempty"`
}

// StaticRoute represents a static route configuration.
type StaticRoute struct {
	// Prefix is the prefix for the static route.
	Prefix string `json:"prefix"`
	// NextHop is the next hop for the static route.
	NextHop *NextHop `json:"nextHop,omitempty"`
	// BFDProfile is the BFD profile for the static route.
	BFDProfile *BFDProfile `json:"bfdProfile,omitempty"`
}

// TrafficMatch represents a traffic match configuration.
type TrafficMatch struct {
	// SrcPrefix is the source prefix to match.
	SrcPrefix *string `json:"srcPrefix,omitempty"`
	// DstPrefix is the destination prefix to match.
	DstPrefix *string `json:"dstPrefix,omitempty"`
	// SrcPort is the source port to match.
	SrcPort *uint16 `json:"srcPort,omitempty"`
	// DstPort is the destination port to match.
	DstPort *uint16 `json:"dstPort,omitempty"`
	// Protocol is the protocol to match.
	Protocol *string `json:"protocol,omitempty"`
}

// PolicyRoute represents a policy-based route configuration.
type PolicyRoute struct {
	// TrafficMatch is the traffic match for the policy route.
	TrafficMatch TrafficMatch `json:"trafficMatch"`
	// NextHop is the next hop for the policy route.
	NextHop NextHop `json:"nextHop"`
}

// MirrorDirection represents the direction of mirrored traffic.
type MirrorDirection string

const (
	// MirrorDirectionIngress represents ingress mirrored traffic.
	MirrorDirectionIngress MirrorDirection = "ingress"
	// MirrorDirectionEgress represents egress mirrored traffic.
	MirrorDirectionEgress MirrorDirection = "egress"
)

// MirrorACL represents a mirror ACL configuration.
type MirrorACL struct {
	// TrafficMatch is the traffic match for the mirror ACL.
	TrafficMatch TrafficMatch `json:"trafficMatch"`
	// MirrorDestination is the name of the interface to mirror to (in most cases a GRE interface).
	MirrorDestination string `json:"mirrorDestination"`
	// Direction is the direction of mirrored traffic.
	// +kubebuilder:validation:Enum=ingress;egress
	Direction MirrorDirection `json:"direction"`
}

// GRELayer represents the GRE encapsulation layer.
type GRELayer string

const (
	// GRELayer2 configures GRE with Layer 2 (Ethernet) encapsulation (GRE TAP).
	GRELayer2 GRELayer = "layer2"
	// GRELayer3 configures GRE with Layer 3 (IP) encapsulation (standard GRE).
	GRELayer3 GRELayer = "layer3"
)

// GRE represents a GRE tunnel interface configuration.
type GRE struct {
	// DestinationAddress is the address of the GRE interface
	DestinationAddress string `json:"destinationAddress"`
	// SourceAddress is the source address of the GRE interface.
	SourceAddress string `json:"sourceAddress"`
	// SourceInterface is the name of the interface that owns the source address
	// (used as the tunnel's link-interface). The kernel validates the tunnel
	// source against this interface, so it must be the loopback carrying
	// SourceAddress; otherwise an IPv6 GRE refuses to originate traffic
	// ("Local address not yet configured").
	SourceInterface string `json:"sourceInterface,omitempty"`
	// Layer is the GRE encapsulation layer.
	// +kubebuilder:validation:Enum=layer2;layer3
	// +kubebuilder:default=layer3
	Layer GRELayer `json:"layer"`
	// EncapsulationKey is the encapsulation key for the GRE interface.
	EncapsulationKey *uint32 `json:"encapsulationKey,omitempty"`
}

// NextHop represents a next hop configuration.
type NextHop struct {
	// Address is the address of the next hop.
	Address *string `json:"address,omitempty"`
	// Vrf is the VRF of the next hop.
	Vrf *string `json:"vrf,omitempty"`
	// Interface is the egress interface for an interface (on-link) next hop, used
	// for routed CNI host routes that point at the moved CRA-side interface.
	Interface *string `json:"interface,omitempty"`
}

// NodeNetworkConfigStatus defines the observed state of NodeConfig.
type NodeNetworkConfigStatus struct {
	// ConfigStatus describes provisioning state of the NodeConfig. Can be either 'provisioning', 'provisioned' or 'invalid'.
	ConfigStatus string `json:"configStatus"`
	// LastUpdate determines when last update (change) of the ConfigStatus field took place.
	LastUpdate metav1.Time `json:"lastUpdate"`
	// LastAppliedRevision stores hash of the NodeConfigRevision that was last applied to the node.
	LastAppliedRevision string `json:"lastAppliedRevision,omitempty"`
	// ErrorMessage contains the error message when ConfigStatus is 'invalid'.
	// This field is cleared whenever ConfigStatus transitions to any non-'invalid' state
	// (including 'provisioning' and 'provisioned'), so stale errors do not persist.
	ErrorMessage string `json:"errorMessage,omitempty"`
	// ASNumber is the local (platform-side) BGP autonomous system number the
	// node agent is configured with, taken from the agent's base config
	// (localASN). It is surfaced here so cluster-wide consumers (e.g. the
	// operator populating BGPPeering.status.asNumber) can read the server ASN
	// without needing access to the base config themselves. Zero means unset.
	// +optional
	ASNumber int64 `json:"asNumber,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=nnc,scope=Cluster
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.configStatus`
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NodeNetworkConfig is the Schema for the node configuration.
// Name of the object is the name of the node.
type NodeNetworkConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeNetworkConfigSpec   `json:"spec,omitempty"`
	Status NodeNetworkConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NodeNetworkConfigList contains a list of NodeConfig.
type NodeNetworkConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeNetworkConfig `json:"items"`
}

func NewEmptyConfig(name string) *NodeNetworkConfig {
	return &NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: NodeNetworkConfigSpec{
			Layer2s:    make(map[string]Layer2),
			FabricVRFs: make(map[string]FabricVRF),
			LocalVRFs:  make(map[string]VRF),
		},
		Status: NodeNetworkConfigStatus{
			ConfigStatus: "",
		},
	}
}

func init() {
	SchemeBuilder.Register(&NodeNetworkConfig{}, &NodeNetworkConfigList{})
}
