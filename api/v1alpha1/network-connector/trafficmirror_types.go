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

// TrafficMirrorSpec defines the desired state of TrafficMirror.
type TrafficMirrorSpec struct {
	// Source identifies the attachment to mirror traffic from.
	// +kubebuilder:validation:Required
	Source MirrorSource `json:"source"`

	// Collector is the name of the Collector resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Collector string `json:"collector"`

	// Direction is the mirror direction.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=ingress;egress;both
	Direction string `json:"direction"`

	// TrafficMatch optionally filters which traffic to mirror.
	// +optional
	TrafficMatch *TrafficMatch `json:"trafficMatch,omitempty"`
}

// TrafficMirrorStatus defines the observed state of TrafficMirror.
type TrafficMirrorStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ActiveNodes is the number of nodes where the mirror is active.
	ActiveNodes int32 `json:"activeNodes,omitempty"`

	// Conditions represent the latest available observations of the TrafficMirror's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=tmir
//+kubebuilder:printcolumn:name="SourceKind",type=string,JSONPath=`.spec.source.kind`
//+kubebuilder:printcolumn:name="SourceName",type=string,JSONPath=`.spec.source.name`
//+kubebuilder:printcolumn:name="Collector",type=string,JSONPath=`.spec.collector`
//+kubebuilder:printcolumn:name="Direction",type=string,JSONPath=`.spec.direction`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TrafficMirror declaratively mirrors traffic from an attachment to a Collector.
type TrafficMirror struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TrafficMirrorSpec   `json:"spec,omitempty"`
	Status TrafficMirrorStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TrafficMirrorList contains a list of TrafficMirror.
type TrafficMirrorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TrafficMirror `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TrafficMirror{}, &TrafficMirrorList{})
}
