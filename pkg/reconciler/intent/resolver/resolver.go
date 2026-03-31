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

package resolver

import (
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	corev1 "k8s.io/api/core/v1"
)

// ResolvedVRF is a VRF with its spec data accessible by name.
type ResolvedVRF struct {
	Name string
	Spec nc.VRFSpec
}

// ResolvedNetwork is a Network with its spec data accessible by name.
type ResolvedNetwork struct {
	Name string
	Spec nc.NetworkSpec
}

// ResolvedDestination is a Destination with its VRF resolved.
type ResolvedDestination struct {
	Name    string
	Spec    nc.DestinationSpec
	VRFSpec *nc.VRFSpec
}

// ResolvedData is the pre-resolved reference graph passed to all builders.
type ResolvedData struct {
	Nodes        []corev1.Node
	VRFs         map[string]*ResolvedVRF
	Networks     map[string]*ResolvedNetwork
	Destinations map[string]*ResolvedDestination

	// RawDestinations preserves the original Destination objects for label matching.
	RawDestinations []nc.Destination

	// Raw intent CRD lists for builders.
	Layer2Attachments    []nc.Layer2Attachment
	Inbounds             []nc.Inbound
	Outbounds            []nc.Outbound
	PodNetworks          []nc.PodNetwork
	BGPPeerings          []nc.BGPPeering
	Collectors           []nc.Collector
	TrafficMirrors       []nc.TrafficMirror
	AnnouncementPolicies []nc.AnnouncementPolicy
}
