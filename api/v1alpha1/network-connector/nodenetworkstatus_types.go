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

// NodeInterfaceState describes the link state of an interface.
// +kubebuilder:validation:Enum=up;down;unknown
type NodeInterfaceState string

const (
	InterfaceStateUp      NodeInterfaceState = "up"
	InterfaceStateDown    NodeInterfaceState = "down"
	InterfaceStateUnknown NodeInterfaceState = "unknown"
)

// NodeInterfaceType describes the type of a network interface.
// +kubebuilder:validation:Enum=physical;bond;vlan;bridge;vxlan;loopback;virtual
type NodeInterfaceType string

const (
	InterfaceTypePhysical NodeInterfaceType = "physical"
	InterfaceTypeBond     NodeInterfaceType = "bond"
	InterfaceTypeVlan     NodeInterfaceType = "vlan"
	InterfaceTypeBridge   NodeInterfaceType = "bridge"
	InterfaceTypeVxlan    NodeInterfaceType = "vxlan"
	InterfaceTypeLoopback NodeInterfaceType = "loopback"
	InterfaceTypeVirtual  NodeInterfaceType = "virtual"
)

// NodeInterface describes a network interface on a node.
type NodeInterface struct {
	// Name is the interface name.
	Name string `json:"name"`
	// Mac is the MAC address of the interface.
	// +optional
	Mac *string `json:"mac,omitempty"`
	// Mtu is the maximum transmission unit.
	// +optional
	Mtu *int32 `json:"mtu,omitempty"`
	// State is the link state.
	State NodeInterfaceState `json:"state"`
	// Type is the interface type.
	// +optional
	Type *NodeInterfaceType `json:"type,omitempty"`
	// Members lists bonded member interfaces (for type=bond).
	// +optional
	Members []string `json:"members,omitempty"`
	// Parent is the parent interface (for type=vlan).
	// +optional
	Parent *string `json:"parent,omitempty"`
	// VlanID is the VLAN ID (for type=vlan).
	// +optional
	VlanID *int32 `json:"vlanID,omitempty"`
	// Addresses lists assigned IP addresses.
	// +optional
	Addresses []string `json:"addresses,omitempty"`
}

// NodeRoute describes a routing table entry on a node.
type NodeRoute struct {
	// Destination is the route destination in CIDR notation.
	Destination string `json:"destination"`
	// Gateway is the next-hop gateway address.
	// +optional
	Gateway *string `json:"gateway,omitempty"`
	// Interface is the egress interface for this route.
	Interface string `json:"interface"`
	// Table is the routing table name.
	// +optional
	Table *string `json:"table,omitempty"`
}

// NodeNetworkStatusSpec is intentionally empty; the resource is agent-populated.
type NodeNetworkStatusSpec struct{}

// NodeNetworkStatusStatus contains the observed network state of a node.
type NodeNetworkStatusStatus struct {
	// Interfaces lists the network interfaces on the node.
	// +optional
	Interfaces []NodeInterface `json:"interfaces,omitempty"`
	// Routes lists the routing table entries on the node.
	// +optional
	Routes []NodeRoute `json:"routes,omitempty"`
	// LastUpdated is the timestamp of the last status update.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`
	// Conditions represent the latest available observations of the resource's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=nns
//+kubebuilder:printcolumn:name="LastUpdated",type="date",JSONPath=`.status.lastUpdated`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NodeNetworkStatus is the Schema for the nodenetworkstatuses API.
// It represents per-node network inventory populated by CRA agents.
type NodeNetworkStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeNetworkStatusSpec   `json:"spec,omitempty"`
	Status NodeNetworkStatusStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NodeNetworkStatusList contains a list of NodeNetworkStatus.
type NodeNetworkStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeNetworkStatus `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeNetworkStatus{}, &NodeNetworkStatusList{})
}
