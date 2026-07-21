/*
Copyright 2025.

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

// NodeRoutedPortsSpec is the desired set of routed CNI attachments that are
// currently live on a node. It is written by the node-local routed-cni agent in
// response to CNI ADD/DEL (delivered over the node's gRPC socket), and consumed
// by the CRA agent, which merges the entries into the NodeNetworkConfig before
// rendering (NETCONF for VSR, netlink for FRR). Storing the state in this
// aggregate per-node object makes it durable across agent restarts and directly
// observable, without a round-trip through the central intent pipeline.
type NodeRoutedPortsSpec struct {
	// Ports is the list of routed attachments currently live on the node.
	Ports []RoutedPortEntry `json:"ports,omitempty"`
}

// RoutedPortEntry is a single routed CNI attachment recorded on a node. The
// identity fields (PodNamespace/PodName/ContainerID/Interface) key the entry so
// CNI ADD upserts and CNI DEL removes exactly one attachment.
// +kubebuilder:validation:XValidation:rule="!has(self.layer2AttachmentRef) || (!has(self.vrf) && !has(self.gatewayV4) && !has(self.gatewayV6) && (!has(self.hostRoutes) || size(self.hostRoutes) == 0))",message="layer2AttachmentRef (L2 attach mode) is mutually exclusive with vrf, gatewayV4, gatewayV6 and hostRoutes"
type RoutedPortEntry struct {
	// PodNamespace is the namespace of the pod owning the attachment.
	PodNamespace string `json:"podNamespace"`
	// PodName is the name of the pod owning the attachment.
	PodName string `json:"podName"`
	// ContainerID is the CNI container ID of the attachment (uniquely identifies
	// the sandbox, so an attachment survives a pod name reuse).
	ContainerID string `json:"containerID"`
	// VRF is the target VRF the port is bound into. Empty (or "default"/"main")
	// means the underlay/default table. Ignored in L2 attach mode (see
	// Layer2AttachmentRef).
	VRF string `json:"vrf,omitempty"`
	// Layer2AttachmentRef, when set, selects L2 attach mode: the port is added as
	// a bridge slave of the Layer2 produced by the referenced Layer2Attachment,
	// instead of being routed. It is mutually exclusive with VRF, GatewayV4,
	// GatewayV6 and HostRoutes (which must be empty in L2 mode).
	// +optional
	Layer2AttachmentRef *Layer2AttachmentRef `json:"layer2AttachmentRef,omitempty"`
	// RoutedPort carries the datapath payload: the moved interface name, on-link
	// gateway addresses and workload host routes.
	RoutedPort `json:",inline"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=nrp,scope=Cluster
//+kubebuilder:printcolumn:name="Ports",type=integer,JSONPath=`.spec.ports[*].interface`,priority=1
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NodeRoutedPorts is the Schema for the per-node routed CNI attachments.
// Name of the object is the name of the node.
type NodeRoutedPorts struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec NodeRoutedPortsSpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

// NodeRoutedPortsList contains a list of NodeRoutedPorts.
type NodeRoutedPortsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeRoutedPorts `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeRoutedPorts{}, &NodeRoutedPortsList{})
}
