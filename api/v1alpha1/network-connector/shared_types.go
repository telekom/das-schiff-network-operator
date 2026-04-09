/*
Copyright 2022.

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

package networkconnector

import corev1 "k8s.io/api/core/v1"

// --- Finalizer Constants ---

const (
	// FinalizerVRFInUse prevents VRF deletion while any Destination references it via vrfRef.
	// Set by the Destination controller on the VRF resource.
	FinalizerVRFInUse = "network-connector.sylvaproject.org/vrf-in-use"

	// FinalizerNetworkInUse prevents Network deletion while any usage CRD references it via networkRef.
	// Set by Layer2Attachment/Inbound/Outbound/PodNetwork controllers on the Network resource.
	FinalizerNetworkInUse = "network-connector.sylvaproject.org/network-in-use"

	// FinalizerDestinationInUse prevents Destination deletion while selected by any usage CRD.
	// Set by Layer2Attachment/Inbound/Outbound/PodNetwork controllers on the Destination resource.
	FinalizerDestinationInUse = "network-connector.sylvaproject.org/destination-in-use"

	// FinalizerCollectorInUse prevents Collector deletion while any TrafficMirror references it.
	// Set by the TrafficMirror controller on the Collector resource.
	FinalizerCollectorInUse = "network-connector.sylvaproject.org/collector-in-use"

	// FinalizerCleanup ensures platform resources are cleaned up before an intent CRD is deleted.
	// Set by a resource's own controller on first reconcile.
	FinalizerCleanup = "network-connector.sylvaproject.org/cleanup"
)

// --- Condition Type Constants ---

const (
	// ConditionTypeReady indicates the resource has been successfully reconciled.
	ConditionTypeReady = "Ready"

	// ConditionTypeResolved indicates all references (networkRef, vrfRef, etc.) have been resolved.
	ConditionTypeResolved = "Resolved"

	// ConditionTypeApplied indicates configuration has been applied to target nodes.
	ConditionTypeApplied = "Applied"

	// ConditionTypeInterfaceNotFound indicates a referenced interface does not exist on a target node.
	ConditionTypeInterfaceNotFound = "InterfaceNotFound"

	// ConditionTypeDuplicateVRF indicates another VRF object in the same namespace
	// declares the same spec.vrf value, causing a conflict.
	ConditionTypeDuplicateVRF = "DuplicateVRF"
)

// --- Annotation Constants ---

const (
	// AnnotationTargetNamespace specifies the hardcoded namespace on the workload cluster
	// where synced intent CRDs are placed.
	AnnotationTargetNamespace = "network-connector.sylvaproject.org/target-namespace"
)

// --- Shared Types ---

// IPNetwork describes an IP address pool for a single address family.
// +kubebuilder:validation:XValidation:rule="isCIDR(self.cidr)",message="cidr must be a valid CIDR notation (e.g. 198.51.100.0/24 or 2001:db8::/32)"
type IPNetwork struct {
	// CIDR is the IP network in CIDR notation (e.g. "198.51.100.0/24").
	// +kubebuilder:validation:Required
	CIDR string `json:"cidr"`

	// PrefixLength is the allocation slice size (e.g. 28 means /28 per consumer).
	// Must be >= the CIDR prefix length.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=128
	PrefixLength *int32 `json:"prefixLength,omitempty"`
}

// AdvertisementConfig configures MetalLB advertisement mode.
type AdvertisementConfig struct {
	// Type is the advertisement mode.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=bgp;l2
	Type string `json:"type"`
}

// NextHopConfig specifies next-hop addresses for static routing in non-HBN mode.
// At least one of IPv4 or IPv6 must be set.
// +kubebuilder:validation:XValidation:rule="has(self.ipv4) || has(self.ipv6)",message="at least one of ipv4 or ipv6 must be set"
type NextHopConfig struct {
	// IPv4 is the IPv4 next-hop address (e.g. "198.51.100.1").
	// +optional
	IPv4 *string `json:"ipv4,omitempty"`

	// IPv6 is the IPv6 next-hop address (e.g. "2001:db8:100::1").
	// +optional
	IPv6 *string `json:"ipv6,omitempty"`
}

// DestinationPort describes a port (or port range) allowed for traffic
// to a Destination's prefixes. Mirrors K8s NetworkPolicy port semantics.
// Exactly one of Port or PortRange must be set per entry.
// +kubebuilder:validation:XValidation:rule="(has(self.port) && !has(self.portRange)) || (!has(self.port) && has(self.portRange))",message="exactly one of port or portRange must be set"
type DestinationPort struct {
	// Protocol is the network protocol.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=TCP;UDP;SCTP
	Protocol corev1.Protocol `json:"protocol"`

	// Port is a single port number (1–65535).
	// Mutually exclusive with PortRange.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port *int32 `json:"port,omitempty"`

	// PortRange specifies a contiguous range of ports.
	// Mutually exclusive with Port.
	// +optional
	PortRange *PortRange `json:"portRange,omitempty"`
}

// PortRange defines an inclusive start–end port range.
// +kubebuilder:validation:XValidation:rule="self.start <= self.end",message="start must be <= end"
type PortRange struct {
	// Start is the first port in the range (1–65535).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Start int32 `json:"start"`

	// End is the last port in the range (≥ Start, 1–65535).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	End int32 `json:"end"`
}

// MirrorSource identifies an attachment to mirror traffic from.
type MirrorSource struct {
	// Kind is the type of attachment.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Layer2Attachment;Inbound;Outbound
	Kind string `json:"kind"`

	// Name is the name of the attachment resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// TrafficMatch optionally filters which traffic to mirror.
// +kubebuilder:validation:XValidation:rule="(!has(self.srcPort) && !has(self.dstPort)) || (has(self.protocol) && self.protocol != 'ICMP')",message="srcPort/dstPort require protocol to be TCP or UDP"
type TrafficMatch struct {
	// SrcPrefix filters by source IP prefix in CIDR notation.
	// +optional
	SrcPrefix *string `json:"srcPrefix,omitempty"`

	// DstPrefix filters by destination IP prefix in CIDR notation.
	// +optional
	DstPrefix *string `json:"dstPrefix,omitempty"`

	// Protocol filters by IP protocol.
	// +optional
	// +kubebuilder:validation:Enum=TCP;UDP;ICMP
	Protocol *string `json:"protocol,omitempty"`

	// SrcPort filters by source port (requires protocol to be tcp or udp).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	SrcPort *int32 `json:"srcPort,omitempty"`

	// DstPort filters by destination port (requires protocol to be tcp or udp).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	DstPort *int32 `json:"dstPort,omitempty"`
}

// AddressAllocation holds allocated IP addresses in status.
type AddressAllocation struct {
	// IPv4 lists allocated IPv4 addresses.
	// +optional
	IPv4 []string `json:"ipv4,omitempty"`

	// IPv6 lists allocated IPv6 addresses.
	// +optional
	IPv6 []string `json:"ipv6,omitempty"`
}

// AdditionalRoute is intentionally removed. All reachable prefixes should be
// modeled as Destination CRDs rather than inline route lists.

// MirrorVRFRef references a VRF CRD for the mirror VRF and specifies its loopback config.
type MirrorVRFRef struct {
	// Name of the VRF resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Loopback defines a loopback interface backed by a CAPI IPAM pool for per-node
	// GRE source IP allocation. The controller provisions IPAddressClaims from the
	// referenced pool on each node in scope.
	// +kubebuilder:validation:Required
	Loopback LoopbackConfig `json:"loopback"`
}

// LoopbackConfig defines a loopback interface backed by a CAPI IPAM pool.
type LoopbackConfig struct {
	// Name is the loopback interface name (e.g. "lo.mir").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// PoolRef references a Cluster API IPAM pool for per-node IP allocation.
	// On the workload cluster this references an InClusterIPPool or similar.
	// Must reference a resource in the same namespace (cross-namespace not supported).
	PoolRef corev1.TypedLocalObjectReference `json:"poolRef"`
}

// BFDProfile represents BFD timer configuration.
// Consistent with the existing BFDProfile in network.t-caas.telekom.com/v1alpha1.
type BFDProfile struct {
	// MinInterval is the minimum interval for BFD packets in milliseconds.
	// +kubebuilder:validation:Minimum=50
	// +kubebuilder:validation:Maximum=60000
	MinInterval uint32 `json:"minInterval"`
}

// BGPAddressFamily specifies a BGP address family for session negotiation.
// +kubebuilder:validation:Enum=ipv4Unicast;ipv6Unicast
type BGPAddressFamily string

const (
	// BGPAddressFamilyIPv4Unicast negotiates IPv4 unicast routes.
	BGPAddressFamilyIPv4Unicast BGPAddressFamily = "ipv4Unicast"
	// BGPAddressFamilyIPv6Unicast negotiates IPv6 unicast routes.
	BGPAddressFamilyIPv6Unicast BGPAddressFamily = "ipv6Unicast"
)
