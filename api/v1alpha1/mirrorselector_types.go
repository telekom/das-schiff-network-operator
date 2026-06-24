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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MirrorSelectorSpec defines what traffic to capture and in which direction.
// It references a MirrorTarget (where to send the mirrored traffic) and a
// MirrorSource (which Layer2NetworkConfiguration or VRFRouteConfiguration to
// capture from).
type MirrorSelectorSpec struct {
	// TrafficMatch selects which packets to mirror. An empty match captures all
	// traffic on the source interface.
	TrafficMatch TrafficMatch `json:"trafficMatch,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self.kind == 'MirrorTarget' && (!has(self.apiGroup) || self.apiGroup == 'network.t-caas.telekom.com')",message="mirrorTarget must reference a MirrorTarget in apiGroup network.t-caas.telekom.com"
	// MirrorTarget references the MirrorTarget that describes the collector.
	MirrorTarget corev1.TypedObjectReference `json:"mirrorTarget"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="(self.kind == 'Layer2NetworkConfiguration' || self.kind == 'VRFRouteConfiguration') && (!has(self.apiGroup) || self.apiGroup == 'network.t-caas.telekom.com')",message="mirrorSource must reference a Layer2NetworkConfiguration or VRFRouteConfiguration in apiGroup network.t-caas.telekom.com"
	// MirrorSource references the Layer2NetworkConfiguration or
	// VRFRouteConfiguration whose traffic is captured.
	MirrorSource corev1.TypedObjectReference `json:"mirrorSource"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=ingress;egress
	// Direction is the direction of traffic to mirror.
	Direction MirrorDirection `json:"direction"`
}

// MirrorSelectorStatus defines the observed state of MirrorSelector.
type MirrorSelectorStatus struct {
	// Conditions represent the latest available observations of the selector
	// state (e.g. Resolved, Applied).
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mirrorselector,scope=Cluster
//+kubebuilder:printcolumn:name="Direction",type=string,JSONPath=`.spec.direction`
//+kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.mirrorSource.name`
//+kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.mirrorTarget.name`

// MirrorSelector is the Schema for the mirrorselectors API. It describes what
// traffic to capture and in which direction.
type MirrorSelector struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MirrorSelectorSpec   `json:"spec,omitempty"`
	Status MirrorSelectorStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MirrorSelectorList contains a list of MirrorSelector.
type MirrorSelectorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MirrorSelector `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MirrorSelector{}, &MirrorSelectorList{})
}
