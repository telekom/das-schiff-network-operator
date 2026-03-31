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
	"fmt"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	corev1 "k8s.io/api/core/v1"
)

// FetchedResources holds all listed intent CRDs and nodes, passed from the reconciler.
type FetchedResources struct {
	Nodes                []corev1.Node
	VRFs                 []nc.VRF
	Networks             []nc.Network
	Destinations         []nc.Destination
	Layer2Attachments    []nc.Layer2Attachment
	Inbounds             []nc.Inbound
	Outbounds            []nc.Outbound
	PodNetworks          []nc.PodNetwork
	BGPPeerings          []nc.BGPPeering
	Collectors           []nc.Collector
	TrafficMirrors       []nc.TrafficMirror
	AnnouncementPolicies []nc.AnnouncementPolicy
}

// ResolveAll resolves all cross-references from fetched resources into a ResolvedData.
func ResolveAll(fetched *FetchedResources) (*ResolvedData, error) {
	vrfs := ResolveVRFs(fetched.VRFs)
	networks := ResolveNetworks(fetched.Networks)

	destinations, err := ResolveDestinations(fetched.Destinations, vrfs)
	if err != nil {
		return nil, fmt.Errorf("error resolving destinations: %w", err)
	}

	return &ResolvedData{
		Nodes:                fetched.Nodes,
		VRFs:                 vrfs,
		Networks:             networks,
		Destinations:         destinations,
		RawDestinations:      fetched.Destinations,
		Layer2Attachments:    fetched.Layer2Attachments,
		Inbounds:             fetched.Inbounds,
		Outbounds:            fetched.Outbounds,
		PodNetworks:          fetched.PodNetworks,
		BGPPeerings:          fetched.BGPPeerings,
		Collectors:           fetched.Collectors,
		TrafficMirrors:       fetched.TrafficMirrors,
		AnnouncementPolicies: fetched.AnnouncementPolicies,
	}, nil
}
