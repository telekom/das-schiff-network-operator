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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// NetworkSpec defines the desired state of Network.
// A Network is a pure pool definition — CIDR, VLAN, VNI, allocation properties.
// It does not carry VRFs, node scope, or usage semantics.
// +kubebuilder:validation:XValidation:rule="has(self.ipv4) || has(self.ipv6) || has(self.vlan)",message="at least one of ipv4, ipv6, or vlan must be specified"
type NetworkSpec struct {
	// IPv4 is the IPv4 address pool for this network.
	// +optional
	IPv4 *IPNetwork `json:"ipv4,omitempty"`

	// IPv6 is the IPv6 address pool for this network.
	// +optional
	IPv6 *IPNetwork `json:"ipv6,omitempty"`

	// VLAN is the VLAN ID for this network.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	VLAN *int32 `json:"vlan,omitempty"`

	// VNI is the VXLAN Network Identifier for this network.
	// In BM4X (bare-metal) mode without SR-IOV, the VNI is provided by the
	// service integration engineer. The node VTEP IP comes from the underlay,
	// not from this CRD.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16777215
	VNI *int32 `json:"vni,omitempty"`
}

// NetworkStatus defines the observed state of Network.
type NetworkStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ReferenceCount is the number of usage CRDs that reference this Network.
	ReferenceCount int32 `json:"referenceCount,omitempty"`

	// Conditions represent the latest available observations of the Network's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=ncnet
//+kubebuilder:printcolumn:name="VLAN",type=integer,JSONPath=`.spec.vlan`
//+kubebuilder:printcolumn:name="VNI",type=integer,JSONPath=`.spec.vni`,priority=10
//+kubebuilder:printcolumn:name="IPv4 CIDR",type=string,JSONPath=`.spec.ipv4.cidr`
//+kubebuilder:printcolumn:name="RefCount",type=integer,JSONPath=`.status.referenceCount`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Network is the Schema for the networks API.
// A Network represents a pure pool definition referenced by name via networkRef
// from usage CRDs such as Layer2Attachment, Inbound, Outbound, and PodNetwork.
type Network struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetworkSpec   `json:"spec,omitempty"`
	Status NetworkStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NetworkList contains a list of Network.
type NetworkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Network `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Network{}, &NetworkList{})
}
