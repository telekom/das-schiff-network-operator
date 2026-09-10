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
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	"github.com/telekom/das-schiff-network-operator/pkg/vrfname"
)

// SBRBuilder auto-detects cross-VRF routing needs and produces intermediate
// LocalVRFs with static routes + ClusterVRF PolicyRoutes for source-based routing.
//
// Single-VRF consumers get a LocalVRF named "s-<hash>" with source-only policy routes.
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
	consumers      []sbrConsumer
}

type sbrConsumer struct {
	kind      string
	namespace string
	name      string
}

// Build produces per-node LocalVRFs and ClusterVRF PolicyRoutes for SBR.
func (b *SBRBuilder) Build(ctx context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	groups := b.collectGroups(data)
	removeInvalidSBRGroups(ctx, groups, data)
	if len(groups) == 0 {
		return nil, nil
	}

	return b.buildContributions(groups, data), nil
}

func (b *SBRBuilder) collectGroups(data *resolver.ResolvedData) map[string]*sbrGroup {
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
		b.addConsumerToGroups(sbrConsumer{mirrorSourceInbound, inb.Namespace, inb.Name}, inb.Spec.Destinations, sources, data, groups)
	}

	// Scan Outbound consumers.
	for i := range data.Outbounds {
		outb := &data.Outbounds[i]
		sources := collectOutboundSources(outb)
		if len(sources) == 0 {
			continue
		}
		b.addConsumerToGroups(sbrConsumer{mirrorSourceOutbound, outb.Namespace, outb.Name}, outb.Spec.Destinations, sources, data, groups)
	}

	// Scan PodNetwork consumers.
	for i := range data.PodNetworks {
		pnet := &data.PodNetworks[i]
		sources := collectPodNetworkSources(pnet, data)
		if len(sources) == 0 {
			continue
		}
		b.addConsumerToGroups(sbrConsumer{"PodNetwork", pnet.Namespace, pnet.Name}, pnet.Spec.Destinations, sources, data, groups)
	}
	return groups
}

func removeInvalidSBRGroups(ctx context.Context, groups map[string]*sbrGroup, data *resolver.ResolvedData) {
	for key, group := range groups {
		err := validateSBRGroup(group, data)
		if err == nil {
			continue
		}
		reason := "ConflictingStaticRoute"
		var skipErr *skipReasonError
		if errors.As(err, &skipErr) {
			reason = skipErr.reason
		}
		for _, consumer := range group.consumers {
			reportSkip(ctx, consumer.kind, consumer.namespace, consumer.name, reason, err.Error())
		}
		delete(groups, key)
	}
}

func (b *SBRBuilder) buildContributions(groups map[string]*sbrGroup, data *resolver.ResolvedData) map[string]*NodeContribution {
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

	return result
}

// addConsumerToGroups resolves a consumer's destination selector and adds its
// source prefixes to the appropriate sbrGroup.
//
// Single-VRF consumers get a dedicated group keyed by VRF name → "s-<hash>".
// Multi-VRF consumers get a combo group keyed by sorted destination names → "s-<hash>".
// Two consumers selecting the same destinations share the same combo group.
func (*SBRBuilder) addConsumerToGroups(
	consumer sbrConsumer,
	destSelector *metav1.LabelSelector,
	sourcePrefixes []string,
	data *resolver.ResolvedData,
	groups map[string]*sbrGroup,
) {
	if destSelector == nil {
		return
	}

	grouped := groupDestinationsByVRF(consumer.namespace, destSelector, data)
	if len(grouped) == 0 {
		return
	}

	if len(grouped) == 1 {
		// Single VRF — use the VRF name as key for a simple "s-<hash>" intermediate.
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
			group.consumers = appendUniqueSBRConsumer(group.consumers, consumer)
			for di := range dests {
				group.vrfRoutes[vrfName] = appendUnique(group.vrfRoutes[vrfName], dests[di].Spec.Prefixes...)
			}
		}
		return
	}

	// Multi-VRF — key by sorted destination names so consumers selecting the
	// same set of destinations share a single combo VRF.
	key := destinationSetKey(consumer.namespace, destSelector, data)
	group, ok := groups[key]
	if !ok {
		group = &sbrGroup{
			key:       key,
			vrfRoutes: make(map[string][]string),
		}
		groups[key] = group
	}

	group.sourcePrefixes = appendUnique(group.sourcePrefixes, sourcePrefixes...)
	group.consumers = appendUniqueSBRConsumer(group.consumers, consumer)
	for vrfName, dests := range grouped {
		for di := range dests {
			group.vrfRoutes[vrfName] = appendUnique(group.vrfRoutes[vrfName], dests[di].Spec.Prefixes...)
		}
	}
}

func appendUniqueSBRConsumer(consumers []sbrConsumer, consumer sbrConsumer) []sbrConsumer {
	for i := range consumers {
		if consumers[i] == consumer {
			return consumers
		}
	}
	return append(consumers, consumer)
}

func validateSBRGroup(group *sbrGroup, data *resolver.ResolvedData) error {
	if err := validateIntermediateVRFName(intermediateVRFName(group), data); err != nil {
		return err
	}
	prefixVRFs := make(map[string]string)
	for vrfName, prefixes := range group.vrfRoutes {
		for _, prefix := range prefixes {
			_, network, err := net.ParseCIDR(prefix)
			if err != nil {
				return fmt.Errorf("invalid destination prefix %q: %w", prefix, err)
			}
			canonical := network.String()
			if existingVRF, exists := prefixVRFs[canonical]; exists && existingVRF != vrfName {
				return fmt.Errorf("destination prefix %q is assigned to multiple VRFs %q and %q",
					canonical, existingVRF, vrfName)
			}
			prefixVRFs[canonical] = vrfName
		}
	}
	return nil
}

// destinationSetKey produces a deterministic key from the sorted names of all
// Destination resources matched by a selector.
func destinationSetKey(namespace string, sel *metav1.LabelSelector, data *resolver.ResolvedData) string {
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return ""
	}
	var names []string
	for i := range data.RawDestinations {
		if data.RawDestinations[i].Namespace == namespace &&
			selector.Matches(labels.Set(data.RawDestinations[i].Labels)) {
			names = append(names, layer2AttachmentDisplayName(namespace, data.RawDestinations[i].Name))
		}
	}
	sort.Strings(names)
	return strings.Join(names, "+")
}

// intermediateVRFName returns the LocalVRF name for a group. Short single-VRF
// keys keep the readable "s-<vrf>" form; keys that would overflow the interface
// name limit and multi-VRF combo keys fall back to "s-<hash>". See
// vrfname.SBRName.
func intermediateVRFName(group *sbrGroup) string {
	return vrfname.SBRName(group.key)
}

// buildComboVRF creates the intermediate LocalVRF for a group.
// It contains static routes for ALL destination prefixes from ALL VRFs in the set,
// each pointing to the correct FabricVRF via NextHop.Vrf. LPM does the disambiguation.
func (*SBRBuilder) buildComboVRF(group *sbrGroup) networkv1alpha1.VRF {
	vrf := networkv1alpha1.VRF{
		VRFImports: []networkv1alpha1.VRFImport{
			{
				FromVRF: clusterVRFName,
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

// collectInboundSources extracts source prefixes from an Inbound (spec or status addresses).
func collectInboundSources(inb *nc.Inbound) []string {
	return collectAddressAllocation(inb.Spec.Addresses, inb.Status.Addresses)
}

// collectOutboundSources extracts source prefixes from an Outbound (spec or status addresses).
func collectOutboundSources(outb *nc.Outbound) []string {
	return collectAddressAllocation(outb.Spec.Addresses, outb.Status.Addresses)
}

// collectPodNetworkSources extracts source CIDRs from a PodNetwork's referenced Network.
func collectPodNetworkSources(pnet *nc.PodNetwork, data *resolver.ResolvedData) []string {
	network, ok := data.Network(pnet.Namespace, pnet.Spec.NetworkRef)
	if !ok {
		return nil
	}

	var sources []string
	if network.Spec.IPv4 != nil {
		sources = append(sources, network.Spec.IPv4.CIDR)
	}
	if network.Spec.IPv6 != nil {
		sources = append(sources, network.Spec.IPv6.CIDR)
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
