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

// DestinationSpec defines the desired state of Destination.
//
// +kubebuilder:validation:XValidation:rule="(has(self.vrfRef) && !has(self.nextHop)) || (!has(self.vrfRef) && has(self.nextHop))",message="exactly one of vrfRef or nextHop must be set"
type DestinationSpec struct {
	// References a VRF resource by name. Mutually exclusive with nextHop.
	VRFRef *string `json:"vrfRef,omitempty"`

	// Subnets reachable via this destination (CIDR notation).
	Prefixes []string `json:"prefixes,omitempty"`

	// Next-hop addresses for static routing. Mutually exclusive with vrfRef.
	NextHop *NextHopConfig `json:"nextHop,omitempty"`

	// Port restrictions for egress NetworkPolicy.
	Ports []DestinationPort `json:"ports,omitempty"`
}

// DestinationStatus defines the observed state of Destination.
type DestinationStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// How many attachments/connections reference this destination.
	ReferenceCount int32 `json:"referenceCount,omitempty"`

	// Standard Kubernetes conditions.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=dst
//+kubebuilder:printcolumn:name="VRFRef",type=string,JSONPath=`.spec.vrfRef`
//+kubebuilder:printcolumn:name="RefCount",type=integer,JSONPath=`.status.referenceCount`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Destination is the Schema for the destinations API.
type Destination struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DestinationSpec   `json:"spec,omitempty"`
	Status DestinationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DestinationList contains a list of Destination.
type DestinationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Destination `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Destination{}, &DestinationList{})
}
