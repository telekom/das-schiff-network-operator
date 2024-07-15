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
	Config   NodeNetworkConfigSpec `json:"config"`
	Revision string                `json:"revision"`
}

type NetworkConfigRevisionStatus struct {
	IsInvalid bool `json:"isInvalid"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=ncr,scope=Cluster
//+kubebuilder:printcolumn:name="Invalid",type=string,JSONPath=`.status.isInvalid`
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NetworkConfig is the Schema for the node configuration.
type NetworkConfigRevision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetworkConfigRevisionSpec   `json:"spec,omitempty"`
	Status NetworkConfigRevisionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NetworkConfigList contains a list of NetworkConfig.
type NetworkConfigRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkConfigRevision `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetworkConfigRevision{}, &NetworkConfigRevisionList{})
}

func NewRevision(config *NodeNetworkConfig) (*NetworkConfigRevision, error) {
	data, err := json.Marshal(config.Spec)
	if err != nil {
		return nil, fmt.Errorf("error marshalling data: %w", err)
	}

	h := sha256.New()
	if _, err := h.Write(data); err != nil {
		return nil, fmt.Errorf("error writing MD5 data: %w", err)
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
