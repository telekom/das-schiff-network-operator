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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetworkConfigSpec defines the desired state of NetworkConfig.
type NetworkConfigRevisionSpec struct {
	// Config stores global configuration of the nodes.
	Config NodeNetworkConfigSpec `json:"config"`
	// Revision is a hash of the NetworkConfigRevision object that is used to identify the particular revision.
	Revision string `json:"revision"`
}

type NetworkConfigRevisionStatus struct {
	// IsInvalid determines if NetworkConfigRevision results in misconfigured nodes (invalid configuration).
	IsInvalid bool `json:"isInvalid"`
	// Ready informs about how many nodes were already provisioned with a config derived from the revision.
	Ready int `json:"ready"`
	// Ongoing informs about how many nodes are currently provisioned with a config derived from the revision.
	Ongoing int `json:"ongoing"`
	// Queued informs about how many nodes are currently waiting to be provisiined with a config derived from the revision.
	Queued int `json:"queued"`
	// Total informs about how many nodes in total can be provisiined with a config derived from the revision.
	Total int `json:"total"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=ncr,scope=Cluster
//+kubebuilder:printcolumn:name="Invalid",type=string,JSONPath=`.status.isInvalid`
//+kubebuilder:printcolumn:name="Queued",type="integer",JSONPath=".status.queued"
//+kubebuilder:printcolumn:name="Ongoing",type="integer",JSONPath=".status.ongoing"
//+kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.ready"
//+kubebuilder:printcolumn:name="Total",type="integer",JSONPath=".status.total"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NetworkConfigRevision is the Schema for the node configuration.
type NetworkConfigRevision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetworkConfigRevisionSpec   `json:"spec,omitempty"`
	Status NetworkConfigRevisionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NetworkConfigRevisionList contains a list of NetworkConfigRevision.
type NetworkConfigRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkConfigRevision `json:"items"`
}

func NewRevision(config *NodeNetworkConfig) (*NetworkConfigRevision, error) {
	data, err := json.Marshal(config.Spec)
	if err != nil {
		return nil, fmt.Errorf("error marshalling data: %w", err)
	}

	h := sha256.New()
	if _, err := h.Write(data); err != nil {
		return nil, fmt.Errorf("failed hashing network config: %w", err)
	}
	hash := h.Sum(nil)
	hashHex := hex.EncodeToString(hash)

	return &NetworkConfigRevision{
		ObjectMeta: metav1.ObjectMeta{Name: hashHex[:10]},
		Spec: NetworkConfigRevisionSpec{
			Config:   config.Spec,
			Revision: hashHex,
		},
		Status: NetworkConfigRevisionStatus{},
	}, nil
}

func init() {
	SchemeBuilder.Register(&NetworkConfigRevision{}, &NetworkConfigRevisionList{})
}
