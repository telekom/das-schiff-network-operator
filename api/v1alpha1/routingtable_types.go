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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// RoutingTableSpec defines the desired state of RoutingTable.
type RoutingTableSpec struct {

	// TableID is the host table that can be used to export routes
	TableID int `json:"tableId"`
}

// RoutingTableStatus defines the observed state of RoutingTable.
type RoutingTableStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=taas,scope=Cluster
//+kubebuilder:printcolumn:name="Table ID",type=integer,JSONPath=`.spec.tableId`

// RoutingTable is the Schema for the routingtables API.
type RoutingTable struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RoutingTableSpec   `json:"spec,omitempty"`
	Status RoutingTableStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RoutingTableList contains a list of RoutingTable.
type RoutingTableList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RoutingTable `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RoutingTable{}, &RoutingTableList{})
}
