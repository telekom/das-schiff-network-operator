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

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BGPPeeringMode describes what this peering session is for.
// +kubebuilder:validation:Enum=listenRange;loopbackPeer
type BGPPeeringMode string

const (
	// BGPPeeringModeListenRange creates a BGP listen-range session for an L2 attachment.
	// The node peers with workloads on the L2 segment.
	BGPPeeringModeListenRange BGPPeeringMode = "listenRange"

	// BGPPeeringModeLoopbackPeer creates a loopback peer BGP session (BGPaaS).
	// A tenant workload (e.g., kube-vip) speaks BGP directly through auto-generated
	// ULA IPv6 loopback addresses.
	BGPPeeringModeLoopbackPeer BGPPeeringMode = "loopbackPeer"
)

// BGPPeeringRef identifies the resources this peering session relates to.
// inboundRefs is always required (both modes need IP pools).
// attachmentRef is required for listenRange mode only.
type BGPPeeringRef struct {
	// AttachmentRef references a Layer2Attachment by name.
	// Required for listenRange mode — identifies the L2 segment for the BGP listen-range.
	// Must not be set for loopbackPeer mode.
	// +optional
	AttachmentRef *string `json:"attachmentRef,omitempty"`

	// InboundRefs references Inbound resources whose IP pools are accepted/advertised.
	// Required for both modes: listenRange uses them as prefix filters,
	// loopbackPeer advertises VIPs from these pools.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	InboundRefs []string `json:"inboundRefs"`
}

// BGPPeeringSpec defines the desired state of BGPPeering.
// +kubebuilder:validation:XValidation:rule="self.mode == 'listenRange' ? has(self.ref.attachmentRef) : !has(self.ref.attachmentRef)",message="attachmentRef is required for listenRange mode and forbidden for loopbackPeer mode"
type BGPPeeringSpec struct {
	// Mode selects the peering type: listenRange (L2 attachment BGP) or loopbackPeer (BGPaaS).
	// Immutable after creation.
	// +kubebuilder:validation:Required
	Mode BGPPeeringMode `json:"mode"`

	// Ref identifies what this peering session is for.
	// +kubebuilder:validation:Required
	Ref BGPPeeringRef `json:"ref"`

	// AdvertiseTransferNetwork controls whether the transfer network prefix
	// is advertised to the BGP peer.
	// +optional
	AdvertiseTransferNetwork *bool `json:"advertiseTransferNetwork,omitempty"`

	// HoldTime is the BGP hold timer duration.
	// +optional
	HoldTime *metav1.Duration `json:"holdTime,omitempty"`

	// KeepaliveTime is the BGP keepalive timer duration.
	// +optional
	KeepaliveTime *metav1.Duration `json:"keepaliveTime,omitempty"`

	// MaximumPrefixes limits the number of prefixes accepted from the peer.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaximumPrefixes *int32 `json:"maximumPrefixes,omitempty"`

	// WorkloadAS is the autonomous system number for the workload/tenant side.
	// Uses asplain notation; for 4-byte ASNs use the full 32-bit integer.
	// When omitted, the controller auto-generates a deterministic ASN.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967295
	WorkloadAS *int64 `json:"workloadAS,omitempty"`

	// AddressFamilies specifies which BGP address families to negotiate.
	// If omitted, defaults to dual-stack (both IPv4 and IPv6 unicast).
	// +optional
	// +kubebuilder:validation:MinItems=1
	AddressFamilies []BGPAddressFamily `json:"addressFamilies,omitempty"`

	// EnableBFD enables Bidirectional Forwarding Detection for fast link-failure
	// detection on this BGP session.
	// +optional
	EnableBFD *bool `json:"enableBFD,omitempty"`

	// BFDProfile configures BFD timer parameters. Only relevant when EnableBFD is true.
	// +optional
	BFDProfile *BFDProfile `json:"bfdProfile,omitempty"`

	// AuthSecretRef references a Secret containing the BGP session password (key: "password").
	// The controller reads the Secret and propagates the password to nodes via
	// NodeNetworkConfig — node agents never need direct Secret RBAC.
	// +optional
	AuthSecretRef *corev1.LocalObjectReference `json:"authSecretRef,omitempty"`
}

// BGPPeeringStatus defines the observed state of BGPPeering.
type BGPPeeringStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ASNumber is the autonomous system number of the platform side (observed).
	ASNumber *int64 `json:"asNumber,omitempty"`

	// NeighborIPs lists the IP addresses used for the BGP session.
	// For loopbackPeer: auto-generated ULA IPv6 addresses.
	// For listenRange: derived from the L2 attachment's transfer network.
	// For SR-IOV with VTEP_NODE: route reflector IPs from infrastructure provisioning.
	// +optional
	NeighborIPs []string `json:"neighborIPs,omitempty"`

	// NeighborASNumber is the AS number of the remote peer (observed).
	NeighborASNumber *int64 `json:"neighborASNumber,omitempty"`

	// WorkloadASNumber is the AS number assigned to the workload (observed/auto-generated).
	WorkloadASNumber *int64 `json:"workloadASNumber,omitempty"`

	// Conditions represent the latest available observations of the
	// BGPPeering's current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=bgpp
//+kubebuilder:validation:XValidation:rule="self.spec.mode == oldSelf.spec.mode",message="mode is immutable"
//+kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
//+kubebuilder:printcolumn:name="WorkloadAS",type=integer,JSONPath=`.spec.workloadAS`
//+kubebuilder:printcolumn:name="BFD",type=boolean,JSONPath=`.spec.enableBFD`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BGPPeering is the Schema for the bgppeerings API.
// It defines a BGP session — either for an L2 attachment (listenRange mode)
// or for tenant BGPaaS (loopbackPeer mode with auto-generated ULA addresses).
type BGPPeering struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BGPPeeringSpec   `json:"spec,omitempty"`
	Status BGPPeeringStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// BGPPeeringList contains a list of BGPPeering.
type BGPPeeringList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BGPPeering `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BGPPeering{}, &BGPPeeringList{})
}
