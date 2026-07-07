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

package networkconnector

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeAttachmentSpec defines the desired state of NodeAttachment.
// A NodeAttachment leaks node IPs into a remote VRF and imports remote prefixes
// back into the cluster VRF via source-based routing.
type NodeAttachmentSpec struct {
	// VRFRef references a VRF CRD by name. The VRF provides the L3VNI and
	// route target for the fabric VRF that node IPs will be exported into.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	VRFRef string `json:"vrfRef"`

	// Destinations selects Destination CRDs whose prefixes should be imported
	// back into the cluster VRF (via SBR) for the attached nodes.
	// +kubebuilder:validation:Required
	Destinations *metav1.LabelSelector `json:"destinations"`

	// NodeSelector restricts which nodes are attached. If omitted, all nodes
	// are attached to the VRF.
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
}

// NodeAttachmentStatus defines the observed state of NodeAttachment.
type NodeAttachmentStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// VRFs lists the VRF names whose prefixes are imported by this attachment,
	// derived from the matched Destinations (spec.destinations →
	// Destination.spec.vrfRef). This is distinct from spec.vrfRef (the target
	// VRF the nodes attach to). Sorted and de-duplicated.
	// +optional
	VRFs []string `json:"vrfs,omitempty"`

	// Conditions represent the latest available observations of the NodeAttachment's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=na
//+kubebuilder:printcolumn:name="VRFRef",type=string,JSONPath=`.spec.vrfRef`
//+kubebuilder:printcolumn:name="VRFs",type=string,JSONPath=`.status.vrfs`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NodeAttachment attaches cluster nodes to a remote VRF. Node IPs (from
// node.status.addresses) are exported as host routes into the VRF, and the
// VRF's destination prefixes are imported back for the attached nodes via SBR.
type NodeAttachment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeAttachmentSpec   `json:"spec,omitempty"`
	Status NodeAttachmentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NodeAttachmentList contains a list of NodeAttachment.
type NodeAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeAttachment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeAttachment{}, &NodeAttachmentList{})
}
