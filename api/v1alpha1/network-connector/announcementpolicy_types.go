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

// RouteAnnouncementConfig configures communities for a class of routes.
type RouteAnnouncementConfig struct {
	// Communities lists BGP community strings to attach to these routes.
	// +optional
	Communities []string `json:"communities,omitempty"`
}

// AggregateConfig controls aggregate (covering prefix) route export behavior.
type AggregateConfig struct {
	// Enabled controls whether an aggregate route is exported alongside host routes.
	// Default: true (auto-computed covering prefix from allocated IPs).
	// Set to false to export only host routes.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Communities attached to the aggregate route.
	// +optional
	Communities []string `json:"communities,omitempty"`

	// PrefixLengthV4 overrides the auto-computed IPv4 aggregate size.
	// Must be between the Network CIDR prefix length and 32.
	// If omitted, controller auto-computes the smallest covering prefix.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=32
	PrefixLengthV4 *int32 `json:"prefixLengthV4,omitempty"`

	// PrefixLengthV6 overrides the auto-computed IPv6 aggregate size.
	// Must be between the Network CIDR prefix length and 128.
	// If omitted, controller auto-computes the smallest covering prefix.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=128
	PrefixLengthV6 *int32 `json:"prefixLengthV6,omitempty"`
}

// AnnouncementPolicySpec defines the desired state of AnnouncementPolicy.
// Host routes (/32, /128) are always exported — the DC fabric needs them as
// more-specifics. This policy controls communities on host routes and whether
// to also export an aggregate covering prefix.
type AnnouncementPolicySpec struct {
	// VRFRef is the VRF this policy governs exports into. Required.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	VRFRef string `json:"vrfRef"`

	// Selector matches usage CRDs (Inbound, Outbound, Layer2Attachment, PodNetwork)
	// by label. The policy applies to exports from matched usage CRDs into the
	// specified VRF. If omitted, applies to ALL usage CRDs exporting into this VRF.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// HostRoutes configures communities for host routes (/32, /128).
	// Host routes are always exported; this controls their community tags.
	// +optional
	HostRoutes *RouteAnnouncementConfig `json:"hostRoutes,omitempty"`

	// Aggregate configures the aggregate (covering prefix) route.
	// Default: enabled with auto-computed prefix from allocated IPs.
	// +optional
	Aggregate *AggregateConfig `json:"aggregate,omitempty"`
}

// AnnouncementPolicyStatus defines the observed state of AnnouncementPolicy.
type AnnouncementPolicyStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// MatchedUsageCRDs is the number of usage CRDs matched by the selector.
	MatchedUsageCRDs int32 `json:"matchedUsageCRDs,omitempty"`

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
//+kubebuilder:resource:shortName=ap
//+kubebuilder:printcolumn:name="VRFRef",type=string,JSONPath=`.spec.vrfRef`
//+kubebuilder:printcolumn:name="Aggregate",type=boolean,JSONPath=`.spec.aggregate.enabled`
//+kubebuilder:printcolumn:name="Matched",type=integer,JSONPath=`.status.matchedUsageCRDs`
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AnnouncementPolicy controls how routes are exported into a VRF: communities
// on host routes and whether to also export an aggregate covering prefix.
type AnnouncementPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AnnouncementPolicySpec   `json:"spec,omitempty"`
	Status AnnouncementPolicyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// AnnouncementPolicyList contains a list of AnnouncementPolicy.
type AnnouncementPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AnnouncementPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AnnouncementPolicy{}, &AnnouncementPolicyList{})
}
