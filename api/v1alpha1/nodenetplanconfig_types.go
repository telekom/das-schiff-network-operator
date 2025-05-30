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
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type NodeNetplanConfigSpec struct {
	DesiredState netplan.State `json:"desiredState,omitempty"`
}

type NodeNetplanConfigStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster

// NodeNetplanConfig is the Schema for the nodenetplanconfigs API.
type NodeNetplanConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeNetplanConfigSpec   `json:"spec,omitempty"`
	Status NodeNetplanConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NodeNetplanConfigList contains a list of NodeNetplanConfig.
type NodeNetplanConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeNetplanConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeNetplanConfig{}, &NodeNetplanConfigList{})
}
