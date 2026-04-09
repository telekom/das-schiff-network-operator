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

// VRFSpec defines the desired state of VRF.
// +kubebuilder:validation:XValidation:rule="self.vrf == oldSelf.vrf",message="vrf is immutable"
type VRFSpec struct {
	// VRF is the name of the VRF in the backbone.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=12
	VRF string `json:"vrf"`

	// VNI is the VXLAN Network Identifier. When omitted, the controller resolves it from operator config.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16777215
	VNI *int32 `json:"vni,omitempty"`

	// RouteTarget is the BGP route target for the VRF. When omitted, the controller resolves it.
	// +optional
	RouteTarget *string `json:"routeTarget,omitempty"`
}

// VRFStatus defines the observed state of VRF.
type VRFStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ReferenceCount is the number of Destinations that reference this VRF.
	ReferenceCount int32 `json:"referenceCount,omitempty"`

	// References lists the names of Destinations that reference this VRF.
	// +optional
	References []string `json:"references,omitempty"`

	// Conditions represent the latest available observations of the VRF's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ncvrf
// +kubebuilder:printcolumn:name="VRF",type=string,JSONPath=`.spec.vrf`
// +kubebuilder:printcolumn:name="VNI",type=integer,JSONPath=`.spec.vni`
// +kubebuilder:printcolumn:name="RouteTarget",type=string,JSONPath=`.spec.routeTarget`
// +kubebuilder:printcolumn:name="RefCount",type=integer,JSONPath=`.status.referenceCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// VRF represents a backbone VRF identity — name and overlay parameters (VNI, route target).
// Defined once per VRF, referenced by name from Destination resources.
type VRF struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VRFSpec   `json:"spec,omitempty"`
	Status VRFStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VRFList contains a list of VRF.
type VRFList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VRF `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VRF{}, &VRFList{})
}
