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

// NodeWorkloadPortsSpec is the desired set of workload CNI attachments that are
// currently live on a node. It is written by the node-local workload-cni agent in
// response to CNI ADD/DEL (delivered over the node's gRPC socket), and consumed
// by the CRA agent, which merges the entries into the NodeNetworkConfig before
// rendering (NETCONF for VSR, netlink for FRR). Storing the state in this
// aggregate per-node object makes it durable across agent restarts and directly
// observable, without a round-trip through the central intent pipeline.
type NodeWorkloadPortsSpec struct {
	// Ports is the list of routed attachments currently live on the node.
	Ports []WorkloadPortEntry `json:"ports,omitempty"`
}

// WorkloadPortEntry is a single workload CNI attachment recorded on a node. An
// entry is keyed by (ContainerID, Interface) — the CNI's own identity of an
// attachment — so CNI ADD upserts and CNI DEL removes exactly one attachment.
// PodNamespace/PodName are informational (for operators correlating ports with
// pods) and not part of the key.
//
// The L2 attach mode rules treat an explicitly empty scalar the same as an
// absent one, so a hand-written object with e.g. `vrf: ""` is not rejected.
// +kubebuilder:validation:XValidation:rule="!has(self.layer2AttachmentRef) || ((!has(self.vrf) || self.vrf == \"\") && (!has(self.gatewayV4) || self.gatewayV4 == \"\") && (!has(self.gatewayV6) || self.gatewayV6 == \"\") && (!has(self.hostRoutes) || size(self.hostRoutes) == 0))",message="layer2AttachmentRef (L2 attach mode) is mutually exclusive with vrf, gatewayV4, gatewayV6 and hostRoutes"
// +kubebuilder:validation:XValidation:rule="!has(self.layer2Trunk) || size(self.layer2Trunk) == 0 || ((!has(self.vrf) || self.vrf == \"\") && (!has(self.gatewayV4) || self.gatewayV4 == \"\") && (!has(self.gatewayV6) || self.gatewayV6 == \"\") && (!has(self.hostRoutes) || size(self.hostRoutes) == 0))",message="layer2Trunk (L2 attach mode) is mutually exclusive with vrf, gatewayV4, gatewayV6 and hostRoutes"
// +kubebuilder:validation:XValidation:rule="!has(self.layer2AttachmentRef) || !has(self.layer2Trunk) || size(self.layer2Trunk) == 0",message="layer2AttachmentRef (untagged access port) and layer2Trunk (tagged trunk) are mutually exclusive"
type WorkloadPortEntry struct {
	// PodNamespace is the namespace of the pod owning the attachment
	// (informational, not part of the entry identity).
	PodNamespace string `json:"podNamespace"`
	// PodName is the name of the pod owning the attachment (informational, not
	// part of the entry identity).
	PodName string `json:"podName"`
	// ContainerID is the CNI container ID of the attachment (uniquely identifies
	// the sandbox, so an attachment survives a pod name reuse).
	ContainerID string `json:"containerID"`
	// VRF is the target VRF the port is bound into. Empty (or "default"/"main")
	// means the underlay/default table. Ignored in L2 attach mode (see
	// Layer2AttachmentRef). The name is rendered into the CRA configuration, so
	// it is limited to a kernel interface name.
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]*$`
	VRF string `json:"vrf,omitempty"`
	// Layer2AttachmentRef, when set, selects L2 attach mode as an untagged access
	// port: the port is added as a bridge slave of the Layer2 produced by the
	// referenced Layer2Attachment, instead of being routed. It is mutually
	// exclusive with VRF, GatewayV4, GatewayV6 and HostRoutes (which must be
	// empty in L2 mode), and with Layer2Trunk.
	// +optional
	Layer2AttachmentRef *Layer2AttachmentRef `json:"layer2AttachmentRef,omitempty"`
	// Layer2Trunk, when non-empty, selects L2 attach mode as an 802.1Q trunk: the
	// port carries one tagged member per entry, each bridged into the Layer2
	// produced by the referenced Layer2Attachment. The port itself is never a
	// bridge slave in this mode, so untagged frames and frames carrying an
	// unmapped VLAN id are not forwarded anywhere. It is mutually exclusive with
	// VRF, GatewayV4, GatewayV6, HostRoutes and Layer2AttachmentRef.
	// Members are keyed by the referenced attachment's namespace and name, so
	// one Layer2 can only be carried once: two tags for the same domain would
	// flood every frame straight back out of the port it came in on. Each
	// member's workload-side VLAN id may be set explicitly (translating it) or
	// inherited from the Layer2 it resolves to; since the inherited ids are only
	// known once the referenced Layer2s are resolved, collisions between
	// workload-side ids are caught when the entry is merged, not here.
	// +optional
	// +listType=map
	// +listMapKey=namespace
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Layer2Trunk []Layer2TrunkMember `json:"layer2Trunk,omitempty"`
	// WorkloadPort carries the datapath payload: the moved interface name, on-link
	// gateway addresses and workload host routes.
	WorkloadPort `json:",inline"`
}

// Layer2TrunkMember is a single tagged member of an L2 trunk attachment: the
// port carries the member's VLAN tag on the workload side, and the frames are
// bridged into the L2 domain of the referenced Layer2Attachment (which may use
// a different VLAN id on the fabric — the tag is translated).
type Layer2TrunkMember struct {
	// Layer2AttachmentRef identifies the Layer2Attachment whose L2 domain this
	// member is bridged into.
	Layer2AttachmentRef `json:",inline"`
	// VLAN is the 802.1Q VLAN id the member is tagged with on the workload side.
	// Unset means the L2 domain's own VLAN id, which is only known once the
	// referenced Layer2 is present on the node, so it is resolved when the entry
	// is merged into the NodeNetworkConfig rather than when it is recorded.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	VLAN *uint16 `json:"vlan,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=nrp,scope=Cluster
//+kubebuilder:printcolumn:name="Ports",type=string,JSONPath=`.spec.ports[*].interface`,priority=1
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NodeWorkloadPorts is the Schema for the per-node workload CNI attachments.
// Name of the object is the name of the node.
type NodeWorkloadPorts struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec NodeWorkloadPortsSpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

// NodeWorkloadPortsList contains a list of NodeWorkloadPorts.
type NodeWorkloadPortsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeWorkloadPorts `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeWorkloadPorts{}, &NodeWorkloadPortsList{})
}
