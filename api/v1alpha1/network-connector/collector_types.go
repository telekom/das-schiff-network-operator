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

// CollectorSpec defines the desired state of Collector.
// +kubebuilder:validation:XValidation:rule="self.protocol == oldSelf.protocol",message="protocol is immutable"
type CollectorSpec struct {
	// Address is the remote collector IP address.
	// +kubebuilder:validation:Required
	Address string `json:"address"`

	// Protocol is the GRE encapsulation type.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=l3gre;l2gre
	Protocol string `json:"protocol"`

	// Key is the GRE tunnel key.
	// +optional
	Key *uint32 `json:"key,omitempty"`

	// MirrorVRF references a VRF for the mirror VRF.
	// +kubebuilder:validation:Required
	MirrorVRF MirrorVRFRef `json:"mirrorVRF"`
}

// CollectorStatus defines the observed state of Collector.
type CollectorStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// GREInterface is the GRE interface name generated.
	// +optional
	GREInterface *string `json:"greInterface,omitempty"`

	// ReferenceCount is the number of TrafficMirrors referencing this Collector.
	ReferenceCount int32 `json:"referenceCount,omitempty"`

	// ActiveNodes is the number of nodes where the collector is active.
	ActiveNodes int32 `json:"activeNodes,omitempty"`

	// Conditions represent the latest available observations of the Collector's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=col
//+kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.spec.address`
//+kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.protocol`
//+kubebuilder:printcolumn:name="MirrorVRF",type=string,JSONPath=`.spec.mirrorVRF.name`
//+kubebuilder:printcolumn:name="RefCount",type=integer,JSONPath=`.status.referenceCount`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Collector defines a GRE collector endpoint and mirror VRF binding.
type Collector struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CollectorSpec   `json:"spec,omitempty"`
	Status CollectorStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// CollectorList contains a list of Collector.
type CollectorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Collector `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Collector{}, &CollectorList{})
}
