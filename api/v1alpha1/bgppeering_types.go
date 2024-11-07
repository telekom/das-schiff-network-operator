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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type BGPPeeringVLAN struct {
	// Name is the name of the Layer2NetworkConfiguration resource
	Name string `json:"name"`
}

// BGPPeeringSpec defines the desired state of BGPPeering.
type BGPPeeringSpec struct {
	// PeeringVlan is the VLAN used for the BGP peering
	PeeringVlan BGPPeeringVLAN `json:"peeringVlan"`

	// RemoteASN is the ASN of the remote BGP peer
	RemoteASN uint32 `json:"remoteASN"`
	// Password is the password used for the BGP peering
	Password *string `json:"password,omitempty"`
	// EnableBFD is the flag to enable BFD for the BGP peering
	EnableBFD bool `json:"enableBFD"`

	// MaximumPrefixes is the maximum number of received prefixes allowed
	MaximumPrefixes *uint32 `json:"maximumPrefixes,omitempty"`

	HoldTime      *metav1.Duration `json:"holdTime,omitempty"`
	KeepaliveTime *metav1.Duration `json:"keepaliveTime,omitempty"`

	// +kubebuilder:validation:MaxItems=4294967295
	// Routes imported from the BGP peer
	Import []VrfRouteConfigurationPrefixItem `json:"import"`

	// +kubebuilder:validation:MaxItems=4294967295
	// Routes exported to the BGP peer
	Export []VrfRouteConfigurationPrefixItem `json:"export"`
}

// BGPPeeringStatus defines the observed state of BGPPeering.
type BGPPeeringStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster
//+kubebuilder:printcolumn:name="VLAN",type=integer,JSONPath=`.spec.peeringVlan.name`
//+kubebuilder:printcolumn:name="ASN",type=string,JSONPath=`.spec.remoteASN`

// BGPPeering is the Schema for the bgppeerings API.
type BGPPeering struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BGPPeeringSpec   `json:"spec,omitempty"`
	Status BGPPeeringStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// BGPPeeringList contains a list of BGPPeering.
type BGPPeeringList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BGPPeering `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BGPPeering{}, &BGPPeeringList{})
}
