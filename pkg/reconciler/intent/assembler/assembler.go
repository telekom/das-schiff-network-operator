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
	"fmt"
	"slices"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/builder"
)

const reservedClusterVRFName = "cluster"

// AssembleResult contains the assembled NNC spec and merged origin tracking data.
type AssembleResult struct {
	Spec    *networkv1alpha1.NodeNetworkConfigSpec
	Origins map[string]string
	// NetplanNodeIPs maps Layer2 keys to per-node IP info for netplan config.
	NetplanNodeIPs map[string]builder.NetplanNodeIP
}

// Assemble merges multiple NodeContributions into a single NodeNetworkConfigSpec.
// Contributions are merged deterministically: Layer2s and VRFs are merged by key,
// ClusterVRF BGPPeers and routes are appended. Origins are merged for traceability.
func Assemble(contributions []*builder.NodeContribution) (*AssembleResult, error) { //nolint:gocognit,cyclop // assembly logic is inherently complex
	spec := &networkv1alpha1.NodeNetworkConfigSpec{
		Layer2s:    make(map[string]networkv1alpha1.Layer2),
		FabricVRFs: make(map[string]networkv1alpha1.FabricVRF),
		LocalVRFs:  make(map[string]networkv1alpha1.VRF),
	}
	origins := make(map[string]string)
	netplanNodeIPs := make(map[string]builder.NetplanNodeIP)

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
			existing.StaticRoutes = mergeStaticRoutes(existing.StaticRoutes, v.StaticRoutes)
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
			existing.StaticRoutes = mergeStaticRoutes(existing.StaticRoutes, v.StaticRoutes)
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
				spec.ClusterVRF.StaticRoutes = mergeStaticRoutes(spec.ClusterVRF.StaticRoutes, c.ClusterVRF.StaticRoutes)
				spec.ClusterVRF.PolicyRoutes = append(spec.ClusterVRF.PolicyRoutes, c.ClusterVRF.PolicyRoutes...)
				spec.ClusterVRF.VRFImports = append(spec.ClusterVRF.VRFImports, c.ClusterVRF.VRFImports...)
			}
		}

		// Merge origins.
		for k, v := range c.Origins {
			origins[k] = v
		}

		// Merge netplan node IPs.
		for k, v := range c.NetplanNodeIPs {
			netplanNodeIPs[k] = v
		}
	}

	if err := validateVRFNames(spec); err != nil {
		return nil, err
	}
	if err := validateStaticRoutes(spec); err != nil {
		return nil, err
	}

	// Drop orphan Layer2 entries that never received a base config (VLAN stays
	// 0). These arise when a mirror-only contribution keys a VLAN whose L2A base
	// entry does not exist on this node — e.g. a non-HBN source, a node outside
	// the L2A's nodeSelector, or an L2A skipped by a build error. A Layer2 with
	// VLAN 0 is rejected by the NNC schema (Minimum=1) and would invalidate the
	// entire NodeNetworkConfig, so it is pruned defensively.
	for k := range spec.Layer2s {
		if spec.Layer2s[k].VLAN == 0 {
			delete(spec.Layer2s, k)
		}
	}

	return &AssembleResult{Spec: spec, Origins: origins, NetplanNodeIPs: netplanNodeIPs}, nil
}

func validateVRFNames(spec *networkv1alpha1.NodeNetworkConfigSpec) error {
	if _, exists := spec.FabricVRFs[reservedClusterVRFName]; exists {
		return fmt.Errorf("FabricVRF %q collides with the reserved cluster VRF", reservedClusterVRFName)
	}
	for name := range spec.LocalVRFs {
		if name == reservedClusterVRFName {
			return fmt.Errorf("LocalVRF %q collides with the reserved cluster VRF", name)
		}
		if _, exists := spec.FabricVRFs[name]; exists {
			return fmt.Errorf("VRF name %q is used by both a FabricVRF and a LocalVRF", name)
		}
	}
	return nil
}

func validateStaticRoutes(spec *networkv1alpha1.NodeNetworkConfigSpec) error {
	if spec.ClusterVRF != nil {
		if err := validateVRFStaticRoutes(reservedClusterVRFName, spec.ClusterVRF.StaticRoutes); err != nil {
			return err
		}
	}
	fabricNames := make([]string, 0, len(spec.FabricVRFs))
	for name := range spec.FabricVRFs {
		fabricNames = append(fabricNames, name)
	}
	slices.Sort(fabricNames)
	for _, name := range fabricNames {
		if err := validateVRFStaticRoutes(name, spec.FabricVRFs[name].StaticRoutes); err != nil {
			return err
		}
	}
	localNames := make([]string, 0, len(spec.LocalVRFs))
	for name := range spec.LocalVRFs {
		localNames = append(localNames, name)
	}
	slices.Sort(localNames)
	for _, name := range localNames {
		if err := validateVRFStaticRoutes(name, spec.LocalVRFs[name].StaticRoutes); err != nil {
			return err
		}
	}
	return nil
}

func validateVRFStaticRoutes(vrfName string, routes []networkv1alpha1.StaticRoute) error {
	nextVRFs := make(map[string]string)
	for i := range routes {
		if routes[i].NextHop == nil || routes[i].NextHop.Vrf == nil {
			continue
		}
		nextVRF := *routes[i].NextHop.Vrf
		if existing, found := nextVRFs[routes[i].Prefix]; found && existing != nextVRF {
			return fmt.Errorf("VRF %q has conflicting next-hop VRFs %q and %q for prefix %q",
				vrfName, existing, nextVRF, routes[i].Prefix)
		}
		nextVRFs[routes[i].Prefix] = nextVRF
	}
	return nil
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

// mergeStaticRoutes deduplicates identical routes and prefers an explicit
// next-hop over a blackhole aggregate for the same prefix. This keeps
// cross-VRF forwarding routes usable when another contribution also announces
// the prefix as an aggregate.
func mergeStaticRoutes(existing, incoming []networkv1alpha1.StaticRoute) []networkv1alpha1.StaticRoute {
	for _, route := range incoming {
		merged := false
		for i := range existing {
			if existing[i].Prefix != route.Prefix {
				continue
			}
			switch {
			case sameStaticRoute(existing[i], route):
				merged = true
			case existing[i].NextHop == nil && route.NextHop != nil:
				existing[i] = route
				merged = true
			case existing[i].NextHop != nil && route.NextHop == nil:
				merged = true
			}
			if merged {
				break
			}
		}
		if !merged {
			existing = append(existing, route)
		}
	}
	return existing
}

func sameStaticRoute(a, b networkv1alpha1.StaticRoute) bool {
	if a.Prefix != b.Prefix {
		return false
	}
	if a.NextHop == nil || b.NextHop == nil {
		return a.NextHop == nil && b.NextHop == nil
	}
	return equalStringPtr(a.NextHop.Address, b.NextHop.Address) &&
		equalStringPtr(a.NextHop.Vrf, b.NextHop.Vrf)
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
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
