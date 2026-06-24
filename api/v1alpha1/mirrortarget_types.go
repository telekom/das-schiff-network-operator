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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MirrorTargetType represents the GRE encapsulation type of a mirror target.
type MirrorTargetType string

const (
	// MirrorTargetTypeL2GRE mirrors traffic via a Layer 2 (Ethernet) GRE tunnel (GRE TAP).
	MirrorTargetTypeL2GRE MirrorTargetType = "l2gre"
	// MirrorTargetTypeL3GRE mirrors traffic via a Layer 3 (IP) GRE tunnel (standard GRE).
	MirrorTargetTypeL3GRE MirrorTargetType = "l3gre"
)

// MirrorTargetSpec defines where mirrored traffic is sent. It describes the GRE
// tunnel properties and which VRF and loopback the tunnel source binds to. The
// per-node source IP is allocated from the referenced loopback's subnet on the
// VRFRouteConfiguration.
type MirrorTargetSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=l2gre;l3gre
	// Type is the GRE encapsulation type of the tunnel.
	Type MirrorTargetType `json:"type"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^((([0-9]{1,3}\.){3}[0-9]{1,3})|(([0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}))$`
	// DestinationIP is the remote collector IP the tunnel points to.
	DestinationIP string `json:"destinationIP"`

	// TunnelKey is an optional GRE encapsulation key.
	TunnelKey *uint32 `json:"key,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=12
	// DestinationVrf is the VRF in which the GRE tunnel lives. It must be a
	// user-created VRFRouteConfiguration (with VNI + route-target).
	DestinationVrf string `json:"destinationVrf"`

	// +kubebuilder:validation:Required
	// SourceLoopback is the name of the loopback (defined on the destination
	// VRF's VRFRouteConfiguration) whose per-node allocated IP is used as the
	// GRE tunnel source address.
	SourceLoopback string `json:"sourceLoopback"`
}

// MirrorTargetStatus defines the observed state of MirrorTarget.
type MirrorTargetStatus struct {
	// ActiveSelectors is the number of MirrorSelectors that reference this target.
	ActiveSelectors int `json:"activeSelectors,omitempty"`
	// ActiveNodes is the number of nodes where the tunnel is configured.
	ActiveNodes int `json:"activeNodes,omitempty"`
	// Conditions represent the latest available observations of the target state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mirrortarget,scope=Cluster
//+kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
//+kubebuilder:printcolumn:name="Destination",type=string,JSONPath=`.spec.destinationIP`
//+kubebuilder:printcolumn:name="VRF",type=string,JSONPath=`.spec.destinationVrf`

// MirrorTarget is the Schema for the mirrortargets API. It describes where
// mirrored traffic is sent (a remote GRE-encapsulated collector).
type MirrorTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MirrorTargetSpec   `json:"spec,omitempty"`
	Status MirrorTargetStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MirrorTargetList contains a list of MirrorTarget.
type MirrorTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MirrorTarget `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MirrorTarget{}, &MirrorTargetList{})
}
