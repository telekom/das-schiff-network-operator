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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OutboundSpec defines the desired state of Outbound.
// Enables egress via SNAT, allocating IPs from a Network for Coil Egress and Calico pools.
// +kubebuilder:validation:XValidation:rule="self.networkRef == oldSelf.networkRef",message="networkRef is immutable"
// +kubebuilder:validation:XValidation:rule="(has(self.count) && !has(self.addresses)) || (!has(self.count) && has(self.addresses))",message="exactly one of count or addresses must be set"
type OutboundSpec struct {
	// NetworkRef references a Network CRD by name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	NetworkRef string `json:"networkRef"`

	// Destinations selects Destination CRDs.
	// +optional
	Destinations *metav1.LabelSelector `json:"destinations,omitempty"`

	// Count is the number of IPs to allocate. Mutually exclusive with Addresses.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Count *int32 `json:"count,omitempty"`

	// Addresses specifies explicit address allocations. Mutually exclusive with Count.
	// +optional
	Addresses *AddressAllocation `json:"addresses,omitempty"`

	// Replicas is the number of Coil egress pod replicas.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`
}

// OutboundStatus defines the observed state of Outbound.
type OutboundStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Addresses holds the allocated IP addresses.
	// +optional
	Addresses *AddressAllocation `json:"addresses,omitempty"`

	// Conditions represent the latest available observations of the Outbound's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=ob
//+kubebuilder:printcolumn:name="NetworkRef",type=string,JSONPath=`.spec.networkRef`
//+kubebuilder:printcolumn:name="Count",type=integer,JSONPath=`.spec.count`
//+kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Outbound enables egress via SNAT, allocating IPs from a Network for Coil Egress and Calico pools.
type Outbound struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OutboundSpec   `json:"spec,omitempty"`
	Status OutboundStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// OutboundList contains a list of Outbound.
type OutboundList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Outbound `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Outbound{}, &OutboundList{})
}
