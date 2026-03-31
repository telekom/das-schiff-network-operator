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

// EthernetConfig defines configuration for an ethernet interface.
type EthernetConfig struct {
	// Mtu is the maximum transmission unit.
	// +optional
	// +kubebuilder:validation:Minimum=1000
	// +kubebuilder:validation:Maximum=9000
	Mtu *int32 `json:"mtu,omitempty"`
	// VirtualFunctionCount is the number of SR-IOV virtual functions to create.
	// +optional
	// +kubebuilder:validation:Minimum=1
	VirtualFunctionCount *int32 `json:"virtualFunctionCount,omitempty"`
}

// BondParameters defines bonding driver parameters.
type BondParameters struct {
	// Mode is the bonding mode.
	// +kubebuilder:validation:Enum="active-backup";"802.3ad";"balance-rr";"balance-xor";"broadcast";"balance-tlb";"balance-alb"
	Mode string `json:"mode"`
	// MiiMonitorInterval is the MII monitoring interval in milliseconds.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MiiMonitorInterval *int32 `json:"miiMonitorInterval,omitempty"`
	// LacpRate is the LACP rate (802.3ad only).
	// +optional
	// +kubebuilder:validation:Enum=fast;slow
	LacpRate *string `json:"lacpRate,omitempty"`
	// UpDelay is the delay before enabling a recovered member in milliseconds.
	// +optional
	// +kubebuilder:validation:Minimum=0
	UpDelay *int32 `json:"upDelay,omitempty"`
	// DownDelay is the delay before disabling a failed member in milliseconds.
	// +optional
	// +kubebuilder:validation:Minimum=0
	DownDelay *int32 `json:"downDelay,omitempty"`
	// TransmitHashPolicy is the hash policy for load balancing.
	// +optional
	// +kubebuilder:validation:Enum=layer2;layer3+4;layer2+3;encap2+3;encap3+4
	TransmitHashPolicy *string `json:"transmitHashPolicy,omitempty"`
}

// BondConfig defines configuration for a bond interface.
type BondConfig struct {
	// Interfaces lists member ethernet interfaces.
	// +kubebuilder:validation:MinItems=1
	Interfaces []string `json:"interfaces"`
	// Mtu is the maximum transmission unit.
	// +optional
	// +kubebuilder:validation:Minimum=1000
	// +kubebuilder:validation:Maximum=9000
	Mtu *int32 `json:"mtu,omitempty"`
	// Parameters are the bonding driver parameters.
	// +optional
	Parameters *BondParameters `json:"parameters,omitempty"`
}

// InterfaceConfigNodeStatus describes the per-node application status.
type InterfaceConfigNodeStatus struct {
	// Node is the name of the node.
	Node string `json:"node"`
	// Applied indicates whether the configuration has been applied.
	Applied bool `json:"applied"`
}

// InterfaceConfigSpec defines the desired state of InterfaceConfig.
type InterfaceConfigSpec struct {
	// NodeSelector selects the target nodes for interface configuration.
	NodeSelector metav1.LabelSelector `json:"nodeSelector"`
	// Ethernets maps interface names to ethernet configurations.
	// +optional
	Ethernets map[string]EthernetConfig `json:"ethernets,omitempty"`
	// Bonds maps bond names to bond configurations.
	// +optional
	Bonds map[string]BondConfig `json:"bonds,omitempty"`
}

// InterfaceConfigStatus defines the observed state of InterfaceConfig.
type InterfaceConfigStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// NodeStatuses lists the per-node application status.
	// +optional
	NodeStatuses []InterfaceConfigNodeStatus `json:"nodeStatuses,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=ifc
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// InterfaceConfig is the Schema for the interfaceconfigs API.
// It defines node-level interface provisioning (bonds, ethernets).
type InterfaceConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InterfaceConfigSpec   `json:"spec,omitempty"`
	Status InterfaceConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// InterfaceConfigList contains a list of InterfaceConfig.
type InterfaceConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InterfaceConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InterfaceConfig{}, &InterfaceConfigList{})
}
