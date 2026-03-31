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

package assembler

import (
	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/builder"
)

// Assemble merges multiple NodeContributions into a single NodeNetworkConfigSpec.
// Contributions are merged deterministically: Layer2s and VRFs are merged by key,
// ClusterVRF BGPPeers and routes are appended.
func Assemble(contributions []*builder.NodeContribution) (*networkv1alpha1.NodeNetworkConfigSpec, error) {
	spec := &networkv1alpha1.NodeNetworkConfigSpec{
		Layer2s:    make(map[string]networkv1alpha1.Layer2),
		FabricVRFs: make(map[string]networkv1alpha1.FabricVRF),
		LocalVRFs:  make(map[string]networkv1alpha1.VRF),
	}

	for _, c := range contributions {
		if c == nil {
			continue
		}

		// Merge Layer2s by key.
		for k, v := range c.Layer2s {
			spec.Layer2s[k] = v
		}

		// Merge FabricVRFs by key, appending nested slices.
		for k, v := range c.FabricVRFs {
			existing, ok := spec.FabricVRFs[k]
			if !ok {
				spec.FabricVRFs[k] = v
				continue
			}
			existing.BGPPeers = append(existing.BGPPeers, v.BGPPeers...)
			existing.StaticRoutes = append(existing.StaticRoutes, v.StaticRoutes...)
			existing.PolicyRoutes = append(existing.PolicyRoutes, v.PolicyRoutes...)
			existing.VRFImports = append(existing.VRFImports, v.VRFImports...)
			existing.MirrorACLs = append(existing.MirrorACLs, v.MirrorACLs...)
			if len(v.Loopbacks) > 0 && existing.Loopbacks == nil {
				existing.Loopbacks = make(map[string]networkv1alpha1.Loopback)
			}
			for lk, lv := range v.Loopbacks {
				existing.Loopbacks[lk] = lv
			}
			spec.FabricVRFs[k] = existing
		}

		// Merge LocalVRFs by key.
		for k, v := range c.LocalVRFs {
			existing, ok := spec.LocalVRFs[k]
			if !ok {
				spec.LocalVRFs[k] = v
				continue
			}
			existing.BGPPeers = append(existing.BGPPeers, v.BGPPeers...)
			existing.StaticRoutes = append(existing.StaticRoutes, v.StaticRoutes...)
			existing.VRFImports = append(existing.VRFImports, v.VRFImports...)
			spec.LocalVRFs[k] = existing
		}

		// Merge ClusterVRF.
		if c.ClusterVRF != nil {
			if spec.ClusterVRF == nil {
				vrfCopy := *c.ClusterVRF
				spec.ClusterVRF = &vrfCopy
			} else {
				spec.ClusterVRF.BGPPeers = append(spec.ClusterVRF.BGPPeers, c.ClusterVRF.BGPPeers...)
				spec.ClusterVRF.StaticRoutes = append(spec.ClusterVRF.StaticRoutes, c.ClusterVRF.StaticRoutes...)
				spec.ClusterVRF.PolicyRoutes = append(spec.ClusterVRF.PolicyRoutes, c.ClusterVRF.PolicyRoutes...)
				spec.ClusterVRF.VRFImports = append(spec.ClusterVRF.VRFImports, c.ClusterVRF.VRFImports...)
			}
		}
	}

	return spec, nil
}
