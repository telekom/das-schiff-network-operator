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
	corev1 "k8s.io/api/core/v1"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

// ResolvedVRF is a VRF with its spec data accessible by name.
type ResolvedVRF struct {
	Namespace string
	Name      string
	Spec      nc.VRFSpec
}

// ResolvedNetwork is a Network with its spec data accessible by name.
type ResolvedNetwork struct {
	Namespace string
	Name      string
	Spec      nc.NetworkSpec
}

// ResolvedDestination is a Destination with its VRF resolved.
type ResolvedDestination struct {
	Namespace string
	Name      string
	Spec      nc.DestinationSpec
	VRFSpec   *nc.VRFSpec
}

// ResolvedData is the pre-resolved reference graph passed to all builders.
type ResolvedData struct {
	Nodes             []corev1.Node
	VRFs              map[string]*ResolvedVRF
	Networks          map[string]*ResolvedNetwork
	Destinations      map[string]*ResolvedDestination
	VRFsByKey         map[string]*ResolvedVRF
	NetworksByKey     map[string]*ResolvedNetwork
	DestinationsByKey map[string]*ResolvedDestination

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
	NodeAttachments      []nc.NodeAttachment

	// BGPPasswords holds resolved BGP session passwords keyed by
	// "<namespace>/<name>" of the BGPPeering.
	BGPPasswords map[string]string
}

// NamespacedKey returns the stable key used for namespaced intent references.
func NamespacedKey(namespace, name string) string {
	return namespace + "\x00" + name
}

// VRF returns a same-namespace VRF. The name-only map fallback keeps direct
// unit-test fixtures compatible when no namespaced index is populated.
func (d *ResolvedData) VRF(namespace, name string) (*ResolvedVRF, bool) {
	if d.VRFsByKey != nil {
		vrf, ok := d.VRFsByKey[NamespacedKey(namespace, name)]
		return vrf, ok
	}
	vrf, ok := d.VRFs[name]
	return vrf, ok
}

// Network returns a same-namespace Network.
func (d *ResolvedData) Network(namespace, name string) (*ResolvedNetwork, bool) {
	if d.NetworksByKey != nil {
		network, ok := d.NetworksByKey[NamespacedKey(namespace, name)]
		return network, ok
	}
	network, ok := d.Networks[name]
	return network, ok
}

// Destination returns a same-namespace Destination.
func (d *ResolvedData) Destination(namespace, name string) (*ResolvedDestination, bool) {
	if d.DestinationsByKey != nil {
		destination, ok := d.DestinationsByKey[NamespacedKey(namespace, name)]
		return destination, ok
	}
	destination, ok := d.Destinations[name]
	return destination, ok
}
