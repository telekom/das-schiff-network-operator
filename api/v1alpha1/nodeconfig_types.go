/*
Copyright 2024.

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
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeConfigSpec defines the desired state of NodeConfig.
type NodeConfigSpec struct {
	Layer2       []Layer2NetworkConfigurationSpec `json:"layer2"`
	Vrf          []VRFRouteConfigurationSpec      `json:"vrf"`
	RoutingTable []RoutingTableSpec               `json:"routingTable"`
}

// NodeConfigStatus defines the observed state of NodeConfig.
type NodeConfigStatus struct {
	ConfigStatus string `json:"configStatus"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=nc,scope=Cluster
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.configStatus`

// NodeConfig is the Schema for the node configuration.
type NodeConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeConfigSpec   `json:"spec,omitempty"`
	Status NodeConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NodeConfigList contains a list of NodeConfig.
type NodeConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeConfig `json:"items"`
}

func (nc *NodeConfig) IsEqual(c *NodeConfig) bool {
	return reflect.DeepEqual(nc.Spec.Layer2, c.Spec.Layer2) && reflect.DeepEqual(nc.Spec.Vrf, c.Spec.Vrf) && reflect.DeepEqual(nc.Spec.RoutingTable, c.Spec.RoutingTable)
}

func NewEmptyConfig(name string) *NodeConfig {
	return &NodeConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: NodeConfigSpec{
			Vrf:          []VRFRouteConfigurationSpec{},
			Layer2:       []Layer2NetworkConfigurationSpec{},
			RoutingTable: []RoutingTableSpec{},
		},
		Status: NodeConfigStatus{
			ConfigStatus: "",
		},
	}
}

func CopyNodeConfig(src, dst *NodeConfig, name string) {
	dst.Spec.Layer2 = make([]Layer2NetworkConfigurationSpec, len(src.Spec.Layer2))
	dst.Spec.Vrf = make([]VRFRouteConfigurationSpec, len(src.Spec.Vrf))
	dst.Spec.RoutingTable = make([]RoutingTableSpec, len(src.Spec.RoutingTable))
	copy(dst.Spec.Layer2, src.Spec.Layer2)
	copy(dst.Spec.Vrf, src.Spec.Vrf)
	copy(dst.Spec.RoutingTable, src.Spec.RoutingTable)
	dst.OwnerReferences = make([]metav1.OwnerReference, len(src.OwnerReferences))
	copy(dst.OwnerReferences, src.OwnerReferences)
	dst.Name = name
}

func init() {
	SchemeBuilder.Register(&NodeConfig{}, &NodeConfigList{})
}
