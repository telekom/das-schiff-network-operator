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

// NodeNetworkConfigSpec defines the desired state of NodeConfig.
type NodeNetworkConfigSpec struct {
	Revision     string                           `json:"revision"`
	Layer2       []Layer2NetworkConfigurationSpec `json:"layer2"`
	Vrf          []VRFRouteConfigurationSpec      `json:"vrf"`
	RoutingTable []RoutingTableSpec               `json:"routingTable"`
}

// NodeNetworkConfigStatus defines the observed state of NodeConfig.
type NodeNetworkConfigStatus struct {
	ConfigStatus string `json:"configStatus"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=nnc,scope=Cluster
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.configStatus`
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NodeNetworkConfig is the Schema for the node configuration.
type NodeNetworkConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeNetworkConfigSpec   `json:"spec,omitempty"`
	Status NodeNetworkConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NodeNetworkConfigList contains a list of NodeConfig.
type NodeNetworkConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeNetworkConfig `json:"items"`
}

func (nc *NodeNetworkConfig) IsEqual(c *NodeNetworkConfig) bool {
	return reflect.DeepEqual(nc.Spec.Layer2, c.Spec.Layer2) && reflect.DeepEqual(nc.Spec.Vrf, c.Spec.Vrf) && reflect.DeepEqual(nc.Spec.RoutingTable, c.Spec.RoutingTable)
}

func NewEmptyConfig(name string) *NodeNetworkConfig {
	return &NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: NodeNetworkConfigSpec{
			Vrf:          []VRFRouteConfigurationSpec{},
			Layer2:       []Layer2NetworkConfigurationSpec{},
			RoutingTable: []RoutingTableSpec{},
		},
		Status: NodeNetworkConfigStatus{
			ConfigStatus: "",
		},
	}
}

func init() {
	SchemeBuilder.Register(&NodeNetworkConfig{}, &NodeNetworkConfigList{})
}
