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

// VRFRouteConfigurationPrefixItem defines a prefix item
type VrfRouteConfigurationPrefixItem struct {
	// +kubebuilder:validation:Required

	// CIDR of the leaked network
	CIDR string `json:"cidr,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967295
	// +kubebuilder:validation:ExclusiveMaximum=false

	// Sequence in the generated prefix-list, if omitted will be list index
	Seq int `json:"seq,omitempty"`

	// Minimum prefix length to be matched
	GE *int `json:"ge,omitempty"`
	// Maximum prefix length to be matched
	LE *int `json:"le,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=permit;deny
	Action string `json:"action"`
}

// VRFRouteConfigurationSpec defines the desired state of VRFRouteConfiguration
type VRFRouteConfigurationSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=12

	// VRF this configuration refers to
	VRF string `json:"vrf,omitempty"`
	// +kubebuilder:validation:MaxItems=4294967295

	// Routes imported from this VRF into the cluster VRF
	Import []VrfRouteConfigurationPrefixItem `json:"import"`
	// +kubebuilder:validation:MaxItems=4294967295

	// Routes exported from the cluster VRF into the specified VRF
	Export []VrfRouteConfigurationPrefixItem `json:"export"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65534
	// +kubebuilder:validation:ExclusiveMaximum=false

	// Sequence of the generated route-map, maximum of 65534 because we sometimes have to set an explicit default-deny
	Seq int `json:"seq"`
}

// VRFRouteConfigurationStatus defines the observed state of VRFRouteConfiguration
type VRFRouteConfigurationStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=vrf,scope=Cluster

// VRFRouteConfiguration is the Schema for the vrfrouteconfigurations API
type VRFRouteConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VRFRouteConfigurationSpec   `json:"spec,omitempty"`
	Status VRFRouteConfigurationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VRFRouteConfigurationList contains a list of VRFRouteConfiguration
type VRFRouteConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VRFRouteConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VRFRouteConfiguration{}, &VRFRouteConfigurationList{})
}
