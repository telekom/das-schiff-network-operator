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

// PodNetworkSpec defines the desired state of PodNetwork.
// +kubebuilder:validation:XValidation:rule="self.networkRef == oldSelf.networkRef",message="networkRef is immutable"
type PodNetworkSpec struct {
	// NetworkRef is the name of the Network resource this PodNetwork allocates from.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	NetworkRef string `json:"networkRef"`

	// Destinations selects the destination workloads that may use this pod network.
	Destinations *metav1.LabelSelector `json:"destinations,omitempty"`
}

// PodNetworkStatus defines the observed state of PodNetwork.
type PodNetworkStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the
	// PodNetwork's current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=pnet
//+kubebuilder:printcolumn:name="NetworkRef",type=string,JSONPath=`.spec.networkRef`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PodNetwork is the Schema for the podnetworks API.
// It allocates additional pod-level networks from a Network for CNI integration.
type PodNetwork struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodNetworkSpec   `json:"spec,omitempty"`
	Status PodNetworkStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PodNetworkList contains a list of PodNetwork.
type PodNetworkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodNetwork `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodNetwork{}, &PodNetworkList{})
}
