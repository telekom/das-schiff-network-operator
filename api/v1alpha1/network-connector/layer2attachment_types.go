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

// SRIOVConfig defines SR-IOV configuration for a Layer2Attachment.
type SRIOVConfig struct {
	// Enabled controls whether SR-IOV is active.
	Enabled bool `json:"enabled"`
}

// NodeIPConfig defines node IP assignment configuration for a Layer2Attachment.
type NodeIPConfig struct {
	// Enabled controls whether node IPs are assigned.
	Enabled bool `json:"enabled"`

	// ReservedForPods is the number of IPs reserved for pods.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ReservedForPods *int32 `json:"reservedForPods,omitempty"`
}

// AnycastStatus holds anycast gateway information.
type AnycastStatus struct {
	// MAC is the anycast gateway MAC address.
	MAC string `json:"mac"`

	// Gateway is the anycast gateway address.
	Gateway string `json:"gateway"`

	// GatewayV4 is the IPv4 anycast gateway address.
	// +optional
	GatewayV4 *string `json:"gatewayV4,omitempty"`
}

// Layer2AttachmentSpec defines the desired state of Layer2Attachment.
// +kubebuilder:validation:XValidation:rule="self.networkRef == oldSelf.networkRef",message="networkRef is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.interfaceName) || self.interfaceName == oldSelf.interfaceName",message="interfaceName is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.sriov) || !has(self.sriov) || self.sriov.enabled == oldSelf.sriov.enabled",message="sriov.enabled is immutable"
type Layer2AttachmentSpec struct {
	// NetworkRef references a Network CRD by name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	NetworkRef string `json:"networkRef"`

	// Destinations selects Destination resources by label.
	// If omitted, no VRF plumbing is performed.
	// +optional
	Destinations *metav1.LabelSelector `json:"destinations,omitempty"`

	// NodeSelector selects which nodes receive this attachment.
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// InterfaceRef is an existing host interface for non-HBN mode (bond, NIC, SR-IOV PF).
	// +optional
	InterfaceRef *string `json:"interfaceRef,omitempty"`

	// InterfaceName is the interface name suffix. Immutable once set.
	// +optional
	InterfaceName *string `json:"interfaceName,omitempty"`

	// MTU is the interface MTU.
	// +optional
	// +kubebuilder:validation:Minimum=1000
	// +kubebuilder:validation:Maximum=9000
	MTU *int32 `json:"mtu,omitempty"`

	// DisableAnycast disables the anycast gateway.
	// +optional
	DisableAnycast *bool `json:"disableAnycast,omitempty"`

	// DisableNeighborSuppression disables neighbor suppression.
	// +optional
	DisableNeighborSuppression *bool `json:"disableNeighborSuppression,omitempty"`

	// DisableSegmentation disables TX/RX segmentation offload on the interface.
	// +optional
	DisableSegmentation *bool `json:"disableSegmentation,omitempty"`

	// SRIOV is the SR-IOV configuration.
	// +optional
	SRIOV *SRIOVConfig `json:"sriov,omitempty"`

	// NodeIPs is the node IP assignment configuration.
	// +optional
	NodeIPs *NodeIPConfig `json:"nodeIPs,omitempty"`

	// Routes defines extra prefixes beyond what matched Destinations provide.
	// +optional
	Routes []AdditionalRoute `json:"routes,omitempty"`
}

// Layer2AttachmentStatus defines the observed state of Layer2Attachment.
type Layer2AttachmentStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SRIOVVlanID is the VLAN ID assigned for SR-IOV device traffic.
	// +optional
	SRIOVVlanID *int32 `json:"sriovVlanID,omitempty"`

	// Anycast holds anycast gateway information.
	// +optional
	Anycast *AnycastStatus `json:"anycast,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// Layer2Attachment attaches a Network as a Layer 2 segment to a set of nodes.
// Supports HBN mode (VXLAN + VRF) and non-HBN mode (physical interface).
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=l2a
//+kubebuilder:printcolumn:name="NetworkRef",type=string,JSONPath=`.spec.networkRef`
//+kubebuilder:printcolumn:name="InterfaceName",type=string,JSONPath=`.spec.interfaceName`
//+kubebuilder:printcolumn:name="MTU",type=integer,JSONPath=`.spec.mtu`
//+kubebuilder:printcolumn:name="SRIOV",type=boolean,JSONPath=`.spec.sriov.enabled`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Layer2Attachment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   Layer2AttachmentSpec   `json:"spec,omitempty"`
	Status Layer2AttachmentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// Layer2AttachmentList contains a list of Layer2Attachment.
type Layer2AttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Layer2Attachment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Layer2Attachment{}, &Layer2AttachmentList{})
}
