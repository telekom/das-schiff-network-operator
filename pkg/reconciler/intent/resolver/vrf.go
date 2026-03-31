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
)

// ResolveVRFs builds a map of VRF name → ResolvedVRF.
func ResolveVRFs(vrfs []nc.VRF) map[string]*ResolvedVRF {
	resolved := make(map[string]*ResolvedVRF, len(vrfs))
	for i := range vrfs {
		resolved[vrfs[i].Name] = &ResolvedVRF{
			Name: vrfs[i].Name,
			Spec: vrfs[i].Spec,
		}
	}
	return resolved
}

// ResolveNetworks builds a map of Network name → ResolvedNetwork.
func ResolveNetworks(networks []nc.Network) map[string]*ResolvedNetwork {
	resolved := make(map[string]*ResolvedNetwork, len(networks))
	for i := range networks {
		resolved[networks[i].Name] = &ResolvedNetwork{
			Name: networks[i].Name,
			Spec: networks[i].Spec,
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
			Name: destinations[i].Name,
			Spec: destinations[i].Spec,
		}

		// VRFRef is optional (Destination may use nextHop instead).
		if destinations[i].Spec.VRFRef != nil {
			vrfName := *destinations[i].Spec.VRFRef
			if vrf, ok := vrfs[vrfName]; ok {
				d.VRFSpec = &vrf.Spec
			} else {
				return nil, fmt.Errorf("destination %q references unknown VRF %q", destinations[i].Name, vrfName)
			}
		}

		resolved[destinations[i].Name] = d
	}
	return resolved, nil
}
