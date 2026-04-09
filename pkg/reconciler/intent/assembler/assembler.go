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

// AssembleResult contains the assembled NNC spec and merged origin tracking data.
type AssembleResult struct {
	Spec    *networkv1alpha1.NodeNetworkConfigSpec
	Origins map[string]string
}

// Assemble merges multiple NodeContributions into a single NodeNetworkConfigSpec.
// Contributions are merged deterministically: Layer2s and VRFs are merged by key,
// ClusterVRF BGPPeers and routes are appended. Origins are merged for traceability.
func Assemble(contributions []*builder.NodeContribution) (*AssembleResult, error) { //nolint:gocognit // assembly logic is inherently complex
	spec := &networkv1alpha1.NodeNetworkConfigSpec{
		Layer2s:    make(map[string]networkv1alpha1.Layer2),
		FabricVRFs: make(map[string]networkv1alpha1.FabricVRF),
		LocalVRFs:  make(map[string]networkv1alpha1.VRF),
	}
	origins := make(map[string]string)

	for _, c := range contributions {
		if c == nil {
			continue
		}

		// Merge Layer2s by key, appending MirrorACLs.
		for k, v := range c.Layer2s {
			existing, ok := spec.Layer2s[k]
			if !ok {
				spec.Layer2s[k] = v
				continue
			}
			// Prefer non-zero scalar fields (the L2A builder sets VLAN/VNI/MTU,
			// while the mirror builder may contribute only MirrorACLs).
			if v.VLAN != 0 {
				existing.VLAN = v.VLAN
			}
			if v.VNI != 0 {
				existing.VNI = v.VNI
			}
			if v.MTU != 0 {
				existing.MTU = v.MTU
			}
			if v.IRB != nil {
				existing.IRB = v.IRB
			}
			existing.MirrorACLs = append(existing.MirrorACLs, v.MirrorACLs...)
			spec.Layer2s[k] = existing
		}

		// Merge FabricVRFs by key, appending nested slices.
		for k := range c.FabricVRFs {
			v := c.FabricVRFs[k]
			existing, ok := spec.FabricVRFs[k]
			if !ok {
				spec.FabricVRFs[k] = v
				continue
			}
			existing.BGPPeers = append(existing.BGPPeers, v.BGPPeers...)
			existing.StaticRoutes = append(existing.StaticRoutes, v.StaticRoutes...)
			existing.PolicyRoutes = append(existing.PolicyRoutes, v.PolicyRoutes...)
			existing.MirrorACLs = append(existing.MirrorACLs, v.MirrorACLs...)

			// Merge VRFImports: deduplicate by FromVRF, merge filter items.
			existing.VRFImports = mergeVRFImports(existing.VRFImports, v.VRFImports)

			if len(v.Loopbacks) > 0 && existing.Loopbacks == nil {
				existing.Loopbacks = make(map[string]networkv1alpha1.Loopback)
			}
			for lk, lv := range v.Loopbacks {
				existing.Loopbacks[lk] = lv
			}
			if v.EVPNExportFilter != nil {
				if existing.EVPNExportFilter == nil {
					existing.EVPNExportFilter = v.EVPNExportFilter
				} else {
					existing.EVPNExportFilter.Items = append(existing.EVPNExportFilter.Items, v.EVPNExportFilter.Items...)
				}
			}
			// Merge EVPN route targets (deduplicated).
			existing.EVPNExportRouteTargets = mergeStringSlice(existing.EVPNExportRouteTargets, v.EVPNExportRouteTargets)
			existing.EVPNImportRouteTargets = mergeStringSlice(existing.EVPNImportRouteTargets, v.EVPNImportRouteTargets)

			if v.Redistribute != nil && existing.Redistribute == nil {
				existing.Redistribute = v.Redistribute
			}
			// Preserve VNI (non-zero wins).
			if existing.VNI == 0 && v.VNI != 0 {
				existing.VNI = v.VNI
			}
			spec.FabricVRFs[k] = existing
		}

		// Merge LocalVRFs by key.
		for k := range c.LocalVRFs {
			v := c.LocalVRFs[k]
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

		// Merge origins.
		for k, v := range c.Origins {
			origins[k] = v
		}
	}

	return &AssembleResult{Spec: spec, Origins: origins}, nil
}

// mergeStringSlice merges two string slices, deduplicating entries.
func mergeStringSlice(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a))
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		if _, exists := seen[s]; !exists {
			a = append(a, s)
			seen[s] = struct{}{}
		}
	}
	return a
}

// mergeVRFImports merges VRFImport slices, deduplicating by FromVRF and merging filter items.
func mergeVRFImports(existing, incoming []networkv1alpha1.VRFImport) []networkv1alpha1.VRFImport {
	byVRF := make(map[string]int) // FromVRF → index in result
	for i, imp := range existing {
		byVRF[imp.FromVRF] = i
	}
	for _, imp := range incoming {
		if idx, ok := byVRF[imp.FromVRF]; ok {
			// Same FromVRF — merge filter items.
			existing[idx].Filter.Items = append(existing[idx].Filter.Items, imp.Filter.Items...)
		} else {
			byVRF[imp.FromVRF] = len(existing)
			existing = append(existing, imp)
		}
	}
	return existing
}
