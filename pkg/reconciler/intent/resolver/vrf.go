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
)

// ResolveVRFs builds a map of VRF name → ResolvedVRF.
func ResolveVRFs(vrfs []nc.VRF) map[string]*ResolvedVRF {
	resolved := make(map[string]*ResolvedVRF, len(vrfs))
	for i := range vrfs {
		resolved[vrfs[i].Name] = &ResolvedVRF{
			Namespace: vrfs[i].Namespace,
			Name:      vrfs[i].Name,
			Spec:      vrfs[i].Spec,
		}
	}
	return resolved
}

// ResolveVRFsByKey builds a namespace/name keyed VRF map.
func ResolveVRFsByKey(vrfs []nc.VRF) map[string]*ResolvedVRF {
	resolved := make(map[string]*ResolvedVRF, len(vrfs))
	for i := range vrfs {
		resolved[NamespacedKey(vrfs[i].Namespace, vrfs[i].Name)] = &ResolvedVRF{
			Namespace: vrfs[i].Namespace,
			Name:      vrfs[i].Name,
			Spec:      vrfs[i].Spec,
		}
	}
	return resolved
}

// ResolveNetworks builds a map of Network name → ResolvedNetwork.
func ResolveNetworks(networks []nc.Network) map[string]*ResolvedNetwork {
	resolved := make(map[string]*ResolvedNetwork, len(networks))
	for i := range networks {
		resolved[networks[i].Name] = &ResolvedNetwork{
			Namespace: networks[i].Namespace,
			Name:      networks[i].Name,
			Spec:      networks[i].Spec,
		}
	}
	return resolved
}

// ResolveNetworksByKey builds a namespace/name keyed Network map.
func ResolveNetworksByKey(networks []nc.Network) map[string]*ResolvedNetwork {
	resolved := make(map[string]*ResolvedNetwork, len(networks))
	for i := range networks {
		resolved[NamespacedKey(networks[i].Namespace, networks[i].Name)] = &ResolvedNetwork{
			Namespace: networks[i].Namespace,
			Name:      networks[i].Name,
			Spec:      networks[i].Spec,
		}
	}
	return resolved
}

// ResolveDestinations builds a map of Destination name → ResolvedDestination,
// looking up each destination's vrfRef in the resolved VRFs.
func ResolveDestinations(destinations []nc.Destination, vrfs map[string]*ResolvedVRF) (map[string]*ResolvedDestination, error) {
	resolved := make(map[string]*ResolvedDestination, len(destinations))
	for i := range destinations {
		d := &ResolvedDestination{
			Namespace: destinations[i].Namespace,
			Name:      destinations[i].Name,
			Spec:      destinations[i].Spec,
		}

		// VRFRef is optional (Destination may use nextHop instead).
		if destinations[i].Spec.VRFRef != nil {
			vrfName := *destinations[i].Spec.VRFRef
			vrf, ok := vrfs[vrfName]
			if !ok {
				continue
			}
			d.VRFSpec = &vrf.Spec
		}

		resolved[destinations[i].Name] = d
	}
	return resolved, nil
}

// ResolveDestinationsByKey builds a namespace/name keyed Destination map and
// resolves each vrfRef only within the Destination's namespace.
func ResolveDestinationsByKey(
	destinations []nc.Destination,
	vrfs map[string]*ResolvedVRF,
) map[string]*ResolvedDestination {
	resolved := make(map[string]*ResolvedDestination, len(destinations))
	for i := range destinations {
		destination := &destinations[i]
		d := &ResolvedDestination{
			Namespace: destination.Namespace,
			Name:      destination.Name,
			Spec:      destination.Spec,
		}
		if destination.Spec.VRFRef != nil {
			if vrf, ok := vrfs[NamespacedKey(destination.Namespace, *destination.Spec.VRFRef)]; ok {
				d.VRFSpec = &vrf.Spec
			}
		}
		resolved[NamespacedKey(destination.Namespace, destination.Name)] = d
	}
	return resolved
}
