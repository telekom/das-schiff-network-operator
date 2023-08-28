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

// Layer2NetworkConfigurationSpec defines the desired state of Layer2NetworkConfiguration.
type Layer2NetworkConfigurationSpec struct {
	// +kubebuilder:validation:Required
	// VLAN Id of the layer 2 network
	ID int `json:"id"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1000
	// +kubebuilder:validation:Maximum=9000
	// +kubebuilder:validation:ExclusiveMaximum=false
	// Network interface MTU
	MTU int `json:"mtu"`

	// +kubebuilder:validation:Required
	// VXLAN VNI Id for the layer 2 network
	VNI int `json:"vni"`

	// +kubebuilder:validation:Pattern=`(?:[[:xdigit:]]{2}:){5}[[:xdigit:]]{2}`
	// If anycast is desired, specify anycast gateway MAC address
	AnycastMac string `json:"anycastMac,omitempty"`

	// Anycast Gateway to configure on bridge
	AnycastGateways []string `json:"anycastGateways,omitempty"`

	// If desired network-operator advertises host routes for local neighbors
	AdvertiseNeighbors bool `json:"advertiseNeighbors,omitempty"`

	// Create MACVLAN attach interface
	CreateMACVLANInterface bool `json:"createMacVLANInterface,omitempty"`

	// Enable ARP / ND suppression
	NeighSuppression *bool `json:"neighSuppression,omitempty"`

	// VRF to attach Layer2 network to, default if not set
	VRF string `json:"vrf,omitempty"`

	// Select nodes to create Layer2 network on
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
}

// Layer2NetworkConfigurationStatus defines the observed state of Layer2NetworkConfiguration.
type Layer2NetworkConfigurationStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=layer2,scope=Cluster

// Layer2NetworkConfiguration is the Schema for the layer2networkconfigurations API.
type Layer2NetworkConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   Layer2NetworkConfigurationSpec   `json:"spec,omitempty"`
	Status Layer2NetworkConfigurationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// Layer2NetworkConfigurationList contains a list of Layer2NetworkConfiguration.
type Layer2NetworkConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Layer2NetworkConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Layer2NetworkConfiguration{}, &Layer2NetworkConfigurationList{})
}
