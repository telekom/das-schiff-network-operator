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

func (nc NodeConfig) IsEqual(c NodeConfig) bool {
	return reflect.DeepEqual(nc.Spec.Layer2, c.Spec.Layer2) && reflect.DeepEqual(nc.Spec.Vrf, c.Spec.Vrf) && reflect.DeepEqual(nc.Spec.RoutingTable, c.Spec.RoutingTable)
}

func init() {
	SchemeBuilder.Register(&NodeConfig{}, &NodeConfigList{})
}
