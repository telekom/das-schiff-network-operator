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
	"fmt"
	"sort"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// SBRBuilder auto-detects cross-VRF routing needs and produces intermediate
// LocalVRFs with static routes + ClusterVRF PolicyRoutes for source-based routing.
//
// Legacy equivalent: pkg/reconciler/operator/vrf.go → updateLocalVRFs().
type SBRBuilder struct{}

// NewSBRBuilder creates a new SBRBuilder.
func NewSBRBuilder() *SBRBuilder {
	return &SBRBuilder{}
}

// Name returns the builder name.
func (b *SBRBuilder) Name() string {
	return "sbr"
}

// sbrEntry tracks source prefixes and destination prefixes per destination VRF.
type sbrEntry struct {
	vrfName        string   // the FabricVRF name (from VRFSpec.VRF)
	sourcePrefixes []string // consumer addresses that need SBR
	destPrefixes   []string // Destination.spec.prefixes reachable via this VRF
}

// Build produces per-node LocalVRFs and ClusterVRF PolicyRoutes for SBR.
func (b *SBRBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	// Collect all SBR needs: map[vrfName]*sbrEntry.
	entries := make(map[string]*sbrEntry)

	// Scan Inbound consumers.
	for i := range data.Inbounds {
		inb := &data.Inbounds[i]
		sources := collectInboundSources(inb)
		if len(sources) == 0 {
			continue
		}
		b.addConsumerEntries(inb.Spec.Destinations, sources, data, entries)
	}

	// Scan Outbound consumers.
	for i := range data.Outbounds {
		outb := &data.Outbounds[i]
		sources := collectOutboundSources(outb)
		if len(sources) == 0 {
			continue
		}
		b.addConsumerEntries(outb.Spec.Destinations, sources, data, entries)
	}

	// Scan PodNetwork consumers.
	for i := range data.PodNetworks {
		pnet := &data.PodNetworks[i]
		sources := collectPodNetworkSources(pnet, data.Networks)
		if len(sources) == 0 {
			continue
		}
		b.addConsumerEntries(pnet.Spec.Destinations, sources, data, entries)
	}

	if len(entries) == 0 {
		return nil, nil
	}

	// Determine if any consumer targets multiple VRFs (requires src+dst matching).
	multiVRFConsumers := b.detectMultiVRFConsumers(data)

	// Build per-node contributions.
	result := make(map[string]*NodeContribution)
	for _, node := range data.Nodes {
		contrib := NewNodeContribution()

		for _, entry := range sortedEntries(entries) {
			intermediateName := fmt.Sprintf("s-%s", entry.vrfName)

			// LocalVRF: static routes to destination prefixes via FabricVRF + cluster import.
			localVRF := b.buildIntermediateVRF(entry)
			existing, ok := contrib.LocalVRFs[intermediateName]
			if ok {
				// Merge static routes and VRFImports if already exists.
				existing.StaticRoutes = append(existing.StaticRoutes, localVRF.StaticRoutes...)
				existing.VRFImports = deduplicateVRFImports(append(existing.VRFImports, localVRF.VRFImports...))
				contrib.LocalVRFs[intermediateName] = existing
			} else {
				contrib.LocalVRFs[intermediateName] = localVRF
			}

			// ClusterVRF PolicyRoutes: steer source traffic to intermediate VRF.
			policyRoutes := b.buildPolicyRoutes(entry, intermediateName, multiVRFConsumers)
			if contrib.ClusterVRF == nil {
				contrib.ClusterVRF = &networkv1alpha1.VRF{}
			}
			contrib.ClusterVRF.PolicyRoutes = append(contrib.ClusterVRF.PolicyRoutes, policyRoutes...)
		}

		if len(contrib.LocalVRFs) > 0 || contrib.ClusterVRF != nil {
			result[node.Name] = contrib
		}
	}

	return result, nil
}

// addConsumerEntries resolves a consumer's destination selector and adds SBR entries
// for each destination VRF found.
func (b *SBRBuilder) addConsumerEntries(
	destSelector *metav1.LabelSelector,
	sourcePrefixes []string,
	data *resolver.ResolvedData,
	entries map[string]*sbrEntry,
) {
	if destSelector == nil {
		return
	}

	grouped := groupDestinationsByVRF(destSelector, data)
	for vrfName, dests := range grouped {
		entry, ok := entries[vrfName]
		if !ok {
			entry = &sbrEntry{vrfName: vrfName}
			entries[vrfName] = entry
		}
		entry.sourcePrefixes = appendUnique(entry.sourcePrefixes, sourcePrefixes...)
		for _, d := range dests {
			entry.destPrefixes = appendUnique(entry.destPrefixes, d.Spec.Prefixes...)
		}
	}
}

// detectMultiVRFConsumers checks if any single consumer targets destinations in
// multiple VRFs. Returns a set of VRF names involved in multi-VRF consumers.
func (b *SBRBuilder) detectMultiVRFConsumers(data *resolver.ResolvedData) map[string]bool {
	multiVRF := make(map[string]bool)

	checkSelector := func(sel *metav1.LabelSelector) {
		if sel == nil {
			return
		}
		grouped := groupDestinationsByVRF(sel, data)
		if len(grouped) > 1 {
			for vrfName := range grouped {
				multiVRF[vrfName] = true
			}
		}
	}

	for i := range data.Inbounds {
		checkSelector(data.Inbounds[i].Spec.Destinations)
	}
	for i := range data.Outbounds {
		checkSelector(data.Outbounds[i].Spec.Destinations)
	}
	for i := range data.PodNetworks {
		checkSelector(data.PodNetworks[i].Spec.Destinations)
	}

	return multiVRF
}

// buildIntermediateVRF creates the LocalVRF for SBR with static routes to destination
// prefixes via the FabricVRF and a cluster VRF import for cluster-internal reachability.
func (b *SBRBuilder) buildIntermediateVRF(entry *sbrEntry) networkv1alpha1.VRF {
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

	// Static routes to destination prefixes — route lookup in the FabricVRF.
	for _, prefix := range entry.destPrefixes {
		vrfName := entry.vrfName
		vrf.StaticRoutes = append(vrf.StaticRoutes, networkv1alpha1.StaticRoute{
			Prefix:  prefix,
			NextHop: &networkv1alpha1.NextHop{Vrf: &vrfName},
		})
	}

	return vrf
}

// buildPolicyRoutes creates ClusterVRF PolicyRoutes that steer source traffic
// to the intermediate VRF.
//
// Single-VRF consumer: source-only match (legacy-compatible).
// Multi-VRF consumer: source+destination match (disambiguation required).
func (b *SBRBuilder) buildPolicyRoutes(entry *sbrEntry, intermediateName string, multiVRF map[string]bool) []networkv1alpha1.PolicyRoute {
	var routes []networkv1alpha1.PolicyRoute

	if multiVRF[entry.vrfName] {
		// Multi-VRF case: need src+dst matching for disambiguation.
		for _, src := range entry.sourcePrefixes {
			for _, dst := range entry.destPrefixes {
				srcCopy := src
				dstCopy := dst
				routes = append(routes, networkv1alpha1.PolicyRoute{
					TrafficMatch: networkv1alpha1.TrafficMatch{
						SrcPrefix: &srcCopy,
						DstPrefix: &dstCopy,
					},
					NextHop: networkv1alpha1.NextHop{Vrf: &intermediateName},
				})
			}
		}
	} else {
		// Single-VRF case: source-only match (legacy-compatible).
		for _, src := range entry.sourcePrefixes {
			srcCopy := src
			routes = append(routes, networkv1alpha1.PolicyRoute{
				TrafficMatch: networkv1alpha1.TrafficMatch{
					SrcPrefix: &srcCopy,
				},
				NextHop: networkv1alpha1.NextHop{Vrf: &intermediateName},
			})
		}
	}

	return routes
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
		// Use the VRF's actual name (spec.vrf), not the CRD resource name.
		resolved, ok := data.Destinations[rawDest.Name]
		if !ok || resolved.VRFSpec == nil {
			continue
		}
		vrfName := resolved.VRFSpec.VRF
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

// sortedEntries returns SBR entries in deterministic order (sorted by VRF name).
func sortedEntries(entries map[string]*sbrEntry) []*sbrEntry {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]*sbrEntry, 0, len(names))
	for _, name := range names {
		result = append(result, entries[name])
	}
	return result
}
