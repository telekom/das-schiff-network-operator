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

// VRFRouteConfigurationPrefixItem defines a prefix item.
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

// VRFRouteConfigurationSpec defines the desired state of VRFRouteConfiguration.
type VRFRouteConfigurationSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// VRF this configuration refers to. May be a readable name; the controller
	// reduces it to a datapath-safe form (<=15 chars) and the webhook rejects
	// names that cannot be reduced to fit.
	VRF string `json:"vrf,omitempty"`

	RouteTarget *string `json:"routeTarget,omitempty"`

	VNI *int `json:"vni,omitempty"`

	// +kubebuilder:validation:MaxItems=4294967295
	// Routes imported from this VRF into the cluster VRF
	Import []VrfRouteConfigurationPrefixItem `json:"import"`

	// +kubebuilder:validation:MaxItems=4294967295
	// Routes exported from the cluster VRF into the specified VRF
	Export []VrfRouteConfigurationPrefixItem `json:"export"`

	// Aggregate Routes that should be announced
	Aggregate []string `json:"aggregate,omitempty"`

	// Traffic from the given prefixes will directly be sent to this VRF
	SBRPrefixes []string `json:"sbrPrefixes,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65534
	// +kubebuilder:validation:ExclusiveMaximum=false
	// Sequence of the generated route-map, maximum of 65534 because we sometimes have to set an explicit default-deny
	Seq int `json:"seq"`

	// Community for export, if omitted no community will be set
	Community *string `json:"community,omitempty"`

	// Select nodes to create VRF on
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// Loopbacks defines loopback interfaces for the VRF with per-node IP
	// allocation from a subnet. The operator allocates a deterministic per-node
	// address from each loopback's subnet (used e.g. as a mirror GRE tunnel
	// source) and advertises it via the VRF's EVPN export filter.
	Loopbacks []VRFLoopback `json:"loopbacks,omitempty"`
}

// VRFLoopback defines a loopback interface inside a VRF whose address is
// allocated per-node from a subnet.
type VRFLoopback struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=15
	// Name is the loopback interface name (e.g. "lo.mir"). It must fit within the
	// Linux interface name length limit (15 characters).
	Name string `json:"name"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^((([0-9]{1,3}\.){3}[0-9]{1,3})|(([0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}))/[0-9]{1,3}$`
	// Subnet is the CIDR from which a unique per-node loopback IP is allocated.
	Subnet string `json:"subnet"`
}

// VRFRouteConfigurationStatus defines the observed state of VRFRouteConfiguration.
type VRFRouteConfigurationStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=vrf,scope=Cluster
//+kubebuilder:printcolumn:name="VRF",type=string,JSONPath=`.spec.vrf`
//+kubebuilder:printcolumn:name="Sequence",type=integer,JSONPath=`.spec.seq`
//+kubebuilder:printcolumn:name="Community",type=string,JSONPath=`.spec.community`

// VRFRouteConfiguration is the Schema for the vrfrouteconfigurations API.
type VRFRouteConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VRFRouteConfigurationSpec   `json:"spec,omitempty"`
	Status VRFRouteConfigurationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VRFRouteConfigurationList contains a list of VRFRouteConfiguration.
type VRFRouteConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VRFRouteConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VRFRouteConfiguration{}, &VRFRouteConfigurationList{})
}
