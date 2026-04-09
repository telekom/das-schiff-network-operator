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

// InboundSpec defines the desired state of Inbound.
// Allocates IPs from a Network for MetalLB pools and BGP/L2 advertisement.
// Optionally exports IPs as host routes into VRFs (HBN mode).
// +kubebuilder:validation:XValidation:rule="self.networkRef == oldSelf.networkRef",message="networkRef is immutable"
// +kubebuilder:validation:XValidation:rule="(has(self.count) && !has(self.addresses)) || (!has(self.count) && has(self.addresses))",message="exactly one of count or addresses must be set"
type InboundSpec struct {
	// NetworkRef references a Network CRD by name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	NetworkRef string `json:"networkRef"`

	// Destinations selects Destination CRDs. If omitted, non-HBN mode is used.
	// +optional
	Destinations *metav1.LabelSelector `json:"destinations,omitempty"`

	// Count is the number of IPs to allocate. Mutually exclusive with Addresses.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Count *int32 `json:"count,omitempty"`

	// Addresses specifies explicit address allocations. Mutually exclusive with Count.
	// +optional
	Addresses *AddressAllocation `json:"addresses,omitempty"`

	// PoolName overrides the MetalLB IPAddressPool name.
	// +optional
	PoolName *string `json:"poolName,omitempty"`

	// TenantLoadBalancerClass specifies the LoadBalancerClass for tenant-managed LB.
	// +optional
	TenantLoadBalancerClass *string `json:"tenantLoadBalancerClass,omitempty"`

	// Advertisement configures the MetalLB advertisement mode (bgp or l2).
	// +kubebuilder:validation:Required
	Advertisement AdvertisementConfig `json:"advertisement"`
}

// InboundStatus defines the observed state of Inbound.
type InboundStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Addresses holds the allocated IP addresses.
	// +optional
	Addresses *AddressAllocation `json:"addresses,omitempty"`

	// PoolName is the resolved MetalLB IPAddressPool name.
	// +optional
	PoolName *string `json:"poolName,omitempty"`

	// Conditions represent the latest available observations of the Inbound's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=ib
//+kubebuilder:printcolumn:name="NetworkRef",type=string,JSONPath=`.spec.networkRef`
//+kubebuilder:printcolumn:name="Count",type=integer,JSONPath=`.spec.count`
//+kubebuilder:printcolumn:name="Pool",type=string,JSONPath=`.status.poolName`
//+kubebuilder:printcolumn:name="Advertisement",type=string,JSONPath=`.spec.advertisement.type`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Inbound allocates IPs from a Network for MetalLB pools and BGP/L2 advertisement.
type Inbound struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InboundSpec   `json:"spec,omitempty"`
	Status InboundStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// InboundList contains a list of Inbound.
type InboundList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Inbound `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Inbound{}, &InboundList{})
}
