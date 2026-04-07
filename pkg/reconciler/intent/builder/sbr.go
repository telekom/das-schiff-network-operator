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

package builder

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// SBRBuilder auto-detects cross-VRF routing needs and produces intermediate
// LocalVRFs with static routes + ClusterVRF PolicyRoutes for source-based routing.
//
// Single-VRF consumers get a LocalVRF named "s-<vrf>" with source-only policy routes.
// Multi-VRF consumers get a single combo LocalVRF named "s-<hash>" that contains
// static routes for all destination prefixes, each pointing to the correct FabricVRF.
// This avoids policy-route ordering issues: ClusterVRF uses src-only policy routes
// and the combo VRF uses regular LPM routing to pick the right FabricVRF.
//
// Legacy equivalent: pkg/reconciler/operator/vrf.go → updateLocalVRFs().
type SBRBuilder struct{}

// NewSBRBuilder creates a new SBRBuilder.
func NewSBRBuilder() *SBRBuilder {
	return &SBRBuilder{}
}

// Name returns the builder name.
func (*SBRBuilder) Name() string {
	return "sbr"
}

// sbrGroup tracks a unique destination set targeted by one or more consumers.
// Consumers resolving to the same set of Destination resources share a single
// intermediate LocalVRF, avoiding duplicate VRFs.
type sbrGroup struct {
	key            string              // sorted destination names joined by "+" (dedup key)
	vrfRoutes      map[string][]string // vrfName → destination prefixes
	sourcePrefixes []string            // consumer source addresses that need SBR
}

// Build produces per-node LocalVRFs and ClusterVRF PolicyRoutes for SBR.
func (b *SBRBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	// groups maps a VRF-set key → sbrGroup.
	// Consumers targeting the same VRF combination share a group.
	groups := make(map[string]*sbrGroup)

	// Scan Inbound consumers.
	for i := range data.Inbounds {
		inb := &data.Inbounds[i]
		sources := collectInboundSources(inb)
		if len(sources) == 0 {
			continue
		}
		b.addConsumerToGroups(inb.Spec.Destinations, sources, data, groups)
	}

	// Scan Outbound consumers.
	for i := range data.Outbounds {
		outb := &data.Outbounds[i]
		sources := collectOutboundSources(outb)
		if len(sources) == 0 {
			continue
		}
		b.addConsumerToGroups(outb.Spec.Destinations, sources, data, groups)
	}

	// Scan PodNetwork consumers.
	for i := range data.PodNetworks {
		pnet := &data.PodNetworks[i]
		sources := collectPodNetworkSources(pnet, data.Networks)
		if len(sources) == 0 {
			continue
		}
		b.addConsumerToGroups(pnet.Spec.Destinations, sources, data, groups)
	}

	if len(groups) == 0 {
		return nil, nil
	}

	// Build per-node contributions.
	result := make(map[string]*NodeContribution)
	for i := range data.Nodes {
		node := &data.Nodes[i]
		contrib := NewNodeContribution()

		for _, group := range sortedGroups(groups) {
			intermediateName := intermediateVRFName(group)

			// LocalVRF: static routes to destination prefixes via respective FabricVRFs + cluster import.
			localVRF := b.buildComboVRF(group)
			existing, ok := contrib.LocalVRFs[intermediateName]
			if ok {
				existing.StaticRoutes = append(existing.StaticRoutes, localVRF.StaticRoutes...)
				existing.VRFImports = deduplicateVRFImports(append(existing.VRFImports, localVRF.VRFImports...))
				contrib.LocalVRFs[intermediateName] = existing
			} else {
				contrib.LocalVRFs[intermediateName] = localVRF
			}

			// ClusterVRF PolicyRoutes: source-only matching.
			// Destination disambiguation is handled by LPM inside the combo VRF.
			if contrib.ClusterVRF == nil {
				contrib.ClusterVRF = &networkv1alpha1.VRF{}
			}
			sort.Strings(group.sourcePrefixes)
			for _, src := range group.sourcePrefixes {
				srcCopy := src
				contrib.ClusterVRF.PolicyRoutes = append(contrib.ClusterVRF.PolicyRoutes, networkv1alpha1.PolicyRoute{
					TrafficMatch: networkv1alpha1.TrafficMatch{
						SrcPrefix: &srcCopy,
					},
					NextHop: networkv1alpha1.NextHop{Vrf: &intermediateName},
				})
			}
		}

		if len(contrib.LocalVRFs) > 0 || contrib.ClusterVRF != nil {
			result[node.Name] = contrib
		}
	}

	return result, nil
}

// addConsumerToGroups resolves a consumer's destination selector and adds its
// source prefixes to the appropriate sbrGroup.
//
// Single-VRF consumers get a dedicated group keyed by VRF name → "s-<vrf>".
// Multi-VRF consumers get a combo group keyed by sorted destination names → "s-<hash>".
// Two consumers selecting the same destinations share the same combo group.
func (*SBRBuilder) addConsumerToGroups(
	destSelector *metav1.LabelSelector,
	sourcePrefixes []string,
	data *resolver.ResolvedData,
	groups map[string]*sbrGroup,
) {
	if destSelector == nil {
		return
	}

	grouped := groupDestinationsByVRF(destSelector, data)
	if len(grouped) == 0 {
		return
	}

	if len(grouped) == 1 {
		// Single VRF — use the VRF name as key for a simple "s-<vrf>" intermediate.
		for vrfName, dests := range grouped {
			group, ok := groups[vrfName]
			if !ok {
				group = &sbrGroup{
					key:       vrfName,
					vrfRoutes: make(map[string][]string),
				}
				groups[vrfName] = group
			}
			group.sourcePrefixes = appendUnique(group.sourcePrefixes, sourcePrefixes...)
			for di := range dests {
				group.vrfRoutes[vrfName] = appendUnique(group.vrfRoutes[vrfName], dests[di].Spec.Prefixes...)
			}
		}
		return
	}

	// Multi-VRF — key by sorted destination names so consumers selecting the
	// same set of destinations share a single combo VRF.
	key := destinationSetKey(destSelector, data)
	group, ok := groups[key]
	if !ok {
		group = &sbrGroup{
			key:       key,
			vrfRoutes: make(map[string][]string),
		}
		groups[key] = group
	}

	group.sourcePrefixes = appendUnique(group.sourcePrefixes, sourcePrefixes...)
	for vrfName, dests := range grouped {
		for di := range dests {
			group.vrfRoutes[vrfName] = appendUnique(group.vrfRoutes[vrfName], dests[di].Spec.Prefixes...)
		}
	}
}

// destinationSetKey produces a deterministic key from the sorted names of all
// Destination resources matched by a selector.
func destinationSetKey(sel *metav1.LabelSelector, data *resolver.ResolvedData) string {
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return ""
	}
	var names []string
	for i := range data.RawDestinations {
		if selector.Matches(labels.Set(data.RawDestinations[i].Labels)) {
			names = append(names, data.RawDestinations[i].Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, "+")
}

// intermediateVRFName returns the LocalVRF name for a group.
// Single-VRF: "s-<vrf>" (backward compatible, human-readable).
// Multi-VRF:  "s-<8-char-hash>" (deterministic, compact).
func intermediateVRFName(group *sbrGroup) string {
	if !strings.Contains(group.key, "+") {
		return fmt.Sprintf("s-%s", group.key)
	}
	h := sha256.Sum256([]byte(group.key))
	return fmt.Sprintf("s-%x", h[:4]) // 8 hex chars
}

// buildComboVRF creates the intermediate LocalVRF for a group.
// It contains static routes for ALL destination prefixes from ALL VRFs in the set,
// each pointing to the correct FabricVRF via NextHop.Vrf. LPM does the disambiguation.
func (*SBRBuilder) buildComboVRF(group *sbrGroup) networkv1alpha1.VRF {
	vrf := networkv1alpha1.VRF{
		VRFImports: []networkv1alpha1.VRFImport{
			{
				FromVRF: "cluster",
				Filter: networkv1alpha1.Filter{
					DefaultAction: networkv1alpha1.Action{
						Type: networkv1alpha1.Accept,
					},
				},
			},
		},
	}

	// Sorted iteration for deterministic output.
	vrfNames := make([]string, 0, len(group.vrfRoutes))
	for name := range group.vrfRoutes {
		vrfNames = append(vrfNames, name)
	}
	sort.Strings(vrfNames)

	for _, vrfName := range vrfNames {
		prefixes := group.vrfRoutes[vrfName]
		sort.Strings(prefixes)
		for _, prefix := range prefixes {
			vn := vrfName
			vrf.StaticRoutes = append(vrf.StaticRoutes, networkv1alpha1.StaticRoute{
				Prefix:  prefix,
				NextHop: &networkv1alpha1.NextHop{Vrf: &vn},
			})
		}
	}

	return vrf
}

// groupDestinationsByVRF resolves a label selector against raw destinations and
// groups ALL matching destinations by their vrfRef. Destinations without vrfRef
// (using nextHop instead) are skipped — no SBR needed for those.
func groupDestinationsByVRF(sel *metav1.LabelSelector, data *resolver.ResolvedData) map[string][]nc.Destination {
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return nil
	}

	grouped := make(map[string][]nc.Destination)
	for i := range data.RawDestinations {
		rawDest := &data.RawDestinations[i]
		if !selector.Matches(labels.Set(rawDest.Labels)) {
			continue
		}
		if rawDest.Spec.VRFRef == nil {
			continue // nextHop-based destination — no SBR needed
		}
		// Use the VRFRef (CRD resource name) as the key so that static-route
		// NextHop.Vrf values produced by buildComboVRF align with the FabricVRF
		// map keys used by the rest of the intent pipeline (mirror, podnetwork,
		// bgppeering builders all key FabricVRFs by VRFRef, not spec.vrf).
		vrfName := *rawDest.Spec.VRFRef
		grouped[vrfName] = append(grouped[vrfName], *rawDest)
	}

	return grouped
}

// collectInboundSources extracts source prefixes from an Inbound (spec or status addresses).
func collectInboundSources(inb *nc.Inbound) []string {
	return collectAddressAllocation(inb.Spec.Addresses, inb.Status.Addresses)
}

// collectOutboundSources extracts source prefixes from an Outbound (spec or status addresses).
func collectOutboundSources(outb *nc.Outbound) []string {
	return collectAddressAllocation(outb.Spec.Addresses, outb.Status.Addresses)
}

// collectPodNetworkSources extracts source CIDRs from a PodNetwork's referenced Network.
func collectPodNetworkSources(pnet *nc.PodNetwork, networks map[string]*resolver.ResolvedNetwork) []string {
	net, ok := networks[pnet.Spec.NetworkRef]
	if !ok {
		return nil
	}

	var sources []string
	if net.Spec.IPv4 != nil {
		sources = append(sources, net.Spec.IPv4.CIDR)
	}
	if net.Spec.IPv6 != nil {
		sources = append(sources, net.Spec.IPv6.CIDR)
	}
	return sources
}

// collectAddressAllocation returns addresses from spec (preferred) or status (IPAM-allocated).
func collectAddressAllocation(spec, status *nc.AddressAllocation) []string {
	alloc := spec
	if alloc == nil {
		alloc = status
	}
	if alloc == nil {
		return nil
	}

	var addrs []string
	// Convert bare IPs to host routes (/32 or /128) for PolicyRoute matching.
	for _, ip := range alloc.IPv4 {
		addrs = append(addrs, ensureCIDR(ip, "/32"))
	}
	for _, ip := range alloc.IPv6 {
		addrs = append(addrs, ensureCIDR(ip, "/128"))
	}
	return addrs
}

// ensureCIDR appends the given suffix if the address doesn't already contain a '/'.
func ensureCIDR(addr, suffix string) string {
	for _, c := range addr {
		if c == '/' {
			return addr
		}
	}
	return addr + suffix
}

// appendUnique appends items to a slice, skipping duplicates.
func appendUnique(existing []string, items ...string) []string {
	seen := make(map[string]bool, len(existing))
	for _, s := range existing {
		seen[s] = true
	}
	for _, s := range items {
		if !seen[s] {
			existing = append(existing, s)
			seen[s] = true
		}
	}
	return existing
}

// deduplicateVRFImports removes duplicate VRFImport entries by FromVRF name.
func deduplicateVRFImports(imports []networkv1alpha1.VRFImport) []networkv1alpha1.VRFImport {
	seen := make(map[string]bool)
	var result []networkv1alpha1.VRFImport
	for _, imp := range imports {
		if !seen[imp.FromVRF] {
			seen[imp.FromVRF] = true
			result = append(result, imp)
		}
	}
	return result
}

// sortedGroups returns SBR groups in deterministic order (sorted by key).
func sortedGroups(groups map[string]*sbrGroup) []*sbrGroup {
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]*sbrGroup, 0, len(keys))
	for _, k := range keys {
		result = append(result, groups[k])
	}
	return result
}
