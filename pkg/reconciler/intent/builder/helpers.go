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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	gonet "net"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

const (
	clusterVRFName = "cluster"

	// mirrorProtocolL2GRE is the Collector GRE encapsulation type for Layer 2 (GRE TAP).
	mirrorProtocolL2GRE = "l2gre"
)

// mirrorGREName returns a deterministic, Linux-safe (<=15 char) GRE interface name
// for a Collector. The same name is referenced by the MirrorACL.MirrorDestination
// emitted by the MirrorBuilder, so both builders must derive it identically.
func mirrorGREName(col *nc.Collector) string {
	sum := sha256.Sum256([]byte(layer2AttachmentDisplayName(col.Namespace, col.Name)))
	h := hex.EncodeToString(sum[:])[:8]
	if col.Spec.Protocol == mirrorProtocolL2GRE {
		return "gtap-" + h
	}
	return "gre-" + h
}

// mirrorGRELayer maps a Collector GRE protocol to the NodeNetworkConfig GRE layer.
func mirrorGRELayer(protocol string) networkv1alpha1.GRELayer {
	if protocol == mirrorProtocolL2GRE {
		return networkv1alpha1.GRELayer2
	}
	return networkv1alpha1.GRELayer3
}

// mirrorGREKey converts an optional Collector GRE key (int64, full uint32 range)
// to the NodeNetworkConfig GRE encapsulation key (*uint32).
func mirrorGREKey(key *int64) *uint32 {
	if key == nil {
		return nil
	}
	v := uint32(*key) //nolint:gosec // validated to 0..4294967295 by the Collector CRD schema
	return &v
}

const (
	ipv4MaxPrefixLen = 32
	ipv6MaxPrefixLen = 128
	ipv4HostRouteLen = 31
	ipv6HostRouteLen = 127
)

// buildFabricVRF creates a base FabricVRF with EVPN export filter and cluster VRFImport,
// both defaulting to Reject (deny-by-default).
func buildFabricVRF(vrfSpec *nc.VRFSpec) networkv1alpha1.FabricVRF {
	fvrf := networkv1alpha1.FabricVRF{
		EVPNExportFilter: &networkv1alpha1.Filter{
			DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		},
		VRF: networkv1alpha1.VRF{
			VRFImports: []networkv1alpha1.VRFImport{
				{
					FromVRF: clusterVRFName,
					Filter: networkv1alpha1.Filter{
						DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
					},
				},
			},
		},
	}

	if vrfSpec.VNI != nil {
		fvrf.VNI = uint32(*vrfSpec.VNI) //nolint:gosec // value validated by CRD schema (positive integer)
	}

	if vrfSpec.RouteTarget != nil {
		fvrf.EVPNImportRouteTargets = []string{*vrfSpec.RouteTarget}
		fvrf.EVPNExportRouteTargets = []string{*vrfSpec.RouteTarget}
	}

	return fvrf
}

// findMatchingAP resolves the single AnnouncementPolicy that applies to a usage CRD.
// It matches by VRF backbone name AND the AP's label selector against the usage CRD's labels.
// Returns nil,nil if no AP matches. Returns an error if more than one matches.
func findMatchingAP(
	namespace string,
	usageCRDLabels map[string]string,
	vrfName string,
	data *resolver.ResolvedData,
) (*nc.AnnouncementPolicy, error) {
	var matches []*nc.AnnouncementPolicy

	for i := range data.AnnouncementPolicies {
		ap := &data.AnnouncementPolicies[i]
		if ap.Namespace != namespace {
			continue
		}

		resolved, ok := data.VRF(namespace, ap.Spec.VRFRef)
		if !ok || resolved.Spec.VRF != vrfName {
			continue
		}

		if ap.Spec.Selector != nil {
			sel, err := metav1.LabelSelectorAsSelector(ap.Spec.Selector)
			if err != nil {
				return nil, fmt.Errorf("AnnouncementPolicy %q has invalid selector: %w", ap.Name, err)
			}
			if !sel.Matches(labels.Set(usageCRDLabels)) {
				continue
			}
		}

		matches = append(matches, ap)
	}

	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.Name)
		}
		return nil, fmt.Errorf("multiple AnnouncementPolicies match: %v", names)
	}
}

// addNetworkToFabricVRF adds a Network's CIDRs to the FabricVRF's EVPN export filter
// and cluster VRFImport filter. The optional AP controls community tagging and
// host-route/aggregate splitting on the EVPN export side. The cluster VRFImport
// always uses plain (community-free) filter items.
func addNetworkToFabricVRF(fvrf *networkv1alpha1.FabricVRF, net *resolver.ResolvedNetwork, ap *nc.AnnouncementPolicy) networkv1alpha1.FabricVRF {
	addNetworkToEVPNExport(fvrf, net, ap)

	// Add to cluster VRFImport filter (plain, no AP communities).
	plainItems := networkCIDRFilterItems(net, nil)
	if len(fvrf.VRFImports) > 0 {
		fvrf.VRFImports[0].Filter.Items = append(fvrf.VRFImports[0].Filter.Items, plainItems...)
	}

	return *fvrf
}

// addNetworkToEVPNExport allows a Network's routes to be advertised from a
// FabricVRF without assuming where those routes originate.
func addNetworkToEVPNExport(fvrf *networkv1alpha1.FabricVRF, net *resolver.ResolvedNetwork, ap *nc.AnnouncementPolicy) {
	evpnItems := networkCIDRFilterItems(net, ap)
	if len(evpnItems) == 0 {
		return
	}

	// Add to EVPN export filter (with AP communities).
	if fvrf.EVPNExportFilter == nil {
		fvrf.EVPNExportFilter = &networkv1alpha1.Filter{
			DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		}
	}
	fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, evpnItems...)
}

// networkCIDRFilterItems creates FilterItems for a Network's IPv4 and IPv6 CIDRs.
// When ap is non-nil, items are split into host-route and non-host-route entries
// with community tags and aggregate behavior from the AP (first-match-wins ordering).
func networkCIDRFilterItems(net *resolver.ResolvedNetwork, ap *nc.AnnouncementPolicy) []networkv1alpha1.FilterItem {
	var items []networkv1alpha1.FilterItem
	if net.Spec.IPv4 != nil && net.Spec.IPv4.CIDR != "" {
		items = append(items, cidrFilterItems(net.Spec.IPv4.CIDR, ipv4MaxPrefixLen, ipv4HostRouteLen, ap)...)
	}
	if net.Spec.IPv6 != nil && net.Spec.IPv6.CIDR != "" {
		items = append(items, cidrFilterItems(net.Spec.IPv6.CIDR, ipv6MaxPrefixLen, ipv6HostRouteLen, ap)...)
	}
	return items
}

// cidrFilterItems creates ordered filter items for a single CIDR.
// Without AP: one item accepting all prefixes within the CIDR.
// With AP: host-route item (ge=max,le=max) then non-host item (le=hostLen),
// each with appropriate communities and accept/reject actions.
func cidrFilterItems(cidr string, maxLen, hostLen int, ap *nc.AnnouncementPolicy) []networkv1alpha1.FilterItem {
	if ap == nil {
		le := maxLen
		return []networkv1alpha1.FilterItem{{
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: cidr, Le: &le},
			},
		}}
	}

	var items []networkv1alpha1.FilterItem

	// 1. Host route item (most specific — matched first in FRR).
	hostAction := networkv1alpha1.Action{Type: networkv1alpha1.Accept}
	if ap.Spec.HostRoutes != nil && len(ap.Spec.HostRoutes.Communities) > 0 {
		additive := true
		hostAction.ModifyRoute = &networkv1alpha1.ModifyRouteAction{
			AddCommunities:      ap.Spec.HostRoutes.Communities,
			AdditiveCommunities: &additive,
		}
	}
	ge := maxLen
	le := maxLen
	items = append(items, networkv1alpha1.FilterItem{
		Matcher: networkv1alpha1.Matcher{
			Prefix: &networkv1alpha1.PrefixMatcher{Prefix: cidr, Ge: &ge, Le: &le},
		},
		Action: hostAction,
	})

	// 2. Non-host route item (aggregate/shorter prefixes).
	leHost := hostLen
	aggEnabled := ap.Spec.Aggregate == nil || ap.Spec.Aggregate.Enabled == nil || *ap.Spec.Aggregate.Enabled

	if !aggEnabled {
		items = append(items, networkv1alpha1.FilterItem{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: cidr, Le: &leHost},
			},
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		})
	} else {
		aggAction := networkv1alpha1.Action{Type: networkv1alpha1.Accept}
		if ap.Spec.Aggregate != nil && len(ap.Spec.Aggregate.Communities) > 0 {
			additive := true
			aggAction.ModifyRoute = &networkv1alpha1.ModifyRouteAction{
				AddCommunities:      ap.Spec.Aggregate.Communities,
				AdditiveCommunities: &additive,
			}
		}
		items = append(items, networkv1alpha1.FilterItem{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: cidr, Le: &leHost},
			},
			Action: aggAction,
		})
	}

	return items
}

// addressFilterItems creates FilterItems for a list of CIDR addresses.
// When ap is non-nil, host route communities from the AP are applied.
func addressFilterItems(addresses []string, ap *nc.AnnouncementPolicy) []networkv1alpha1.FilterItem {
	items := make([]networkv1alpha1.FilterItem, 0, len(addresses))
	for _, addr := range addresses {
		le := ipv4MaxPrefixLen
		suffix := "/32"
		if strings.Contains(addr, ":") {
			le = ipv6MaxPrefixLen
			suffix = "/128"
		}
		prefix := ensureCIDR(addr, suffix)
		action := networkv1alpha1.Action{Type: networkv1alpha1.Accept}
		if ap != nil && ap.Spec.HostRoutes != nil && len(ap.Spec.HostRoutes.Communities) > 0 {
			additive := true
			action.ModifyRoute = &networkv1alpha1.ModifyRouteAction{
				AddCommunities:      ap.Spec.HostRoutes.Communities,
				AdditiveCommunities: &additive,
			}
		}
		items = append(items, networkv1alpha1.FilterItem{
			Action: action,
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: prefix, Le: &le},
			},
		})
	}
	return items
}

// matchNodes returns nodes matching a label selector. If selector is nil, all nodes match.
func matchNodes(nodes []corev1.Node, selector *metav1.LabelSelector) ([]corev1.Node, error) {
	if selector == nil {
		return nodes, nil
	}

	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	var matched []corev1.Node
	for i := range nodes {
		if sel.Matches(labels.Set(nodes[i].Labels)) {
			matched = append(matched, nodes[i])
		}
	}

	return matched, nil
}

// addAggregateRoutes adds the Network CIDR(s) as aggregate static routes to the FabricVRF
// and adds matching entries to the EVPN export filter so the aggregates are announced.
// By default, the covering prefix is always added so the fabric can export it via EVPN.
// When an AP is provided its aggregate config controls prefix length and suppression.
func addAggregateRoutes(fvrf *networkv1alpha1.FabricVRF, net *resolver.ResolvedNetwork, ap *nc.AnnouncementPolicy) {
	addAggregateRoutesWithNextHop(fvrf, net, ap, nil)
}

// addAggregateRoutesViaVRF adds aggregate routes that forward through another
// VRF instead of blackholing locally. Multi-VRF L2 attachments use this so a
// more-specific aggregate cannot override the return route to their combo VRF.
func addAggregateRoutesViaVRF(fvrf *networkv1alpha1.FabricVRF, net *resolver.ResolvedNetwork, ap *nc.AnnouncementPolicy, vrfName string) {
	nextVRF := vrfName
	addAggregateRoutesWithNextHop(fvrf, net, ap, &networkv1alpha1.NextHop{Vrf: &nextVRF})
}

func addAggregateRoutesWithNextHop(
	fvrf *networkv1alpha1.FabricVRF,
	net *resolver.ResolvedNetwork,
	ap *nc.AnnouncementPolicy,
	nextHop *networkv1alpha1.NextHop,
) {
	if ap != nil && ap.Spec.Aggregate != nil && ap.Spec.Aggregate.Enabled != nil && !*ap.Spec.Aggregate.Enabled {
		return
	}

	var aggCfg *nc.AggregateConfig
	if ap != nil {
		aggCfg = ap.Spec.Aggregate
	}

	action := networkv1alpha1.Action{Type: networkv1alpha1.Accept}
	if ap != nil && ap.Spec.Aggregate != nil && len(ap.Spec.Aggregate.Communities) > 0 {
		additive := true
		action.ModifyRoute = &networkv1alpha1.ModifyRouteAction{
			AddCommunities:      ap.Spec.Aggregate.Communities,
			AdditiveCommunities: &additive,
		}
	}

	if net.Spec.IPv4 != nil && net.Spec.IPv4.CIDR != "" {
		var overrideLen *int32
		if aggCfg != nil {
			overrideLen = aggCfg.PrefixLengthV4
		}
		prefix := computeAggregatePrefix(net.Spec.IPv4.CIDR, overrideLen)
		fvrf.StaticRoutes = appendUniqueStaticRoute(fvrf.StaticRoutes, networkv1alpha1.StaticRoute{
			Prefix:  prefix,
			NextHop: nextHop,
		})
		if fvrf.EVPNExportFilter != nil {
			fvrf.EVPNExportFilter.Items = appendUniqueFilterItem(fvrf.EVPNExportFilter.Items, networkv1alpha1.FilterItem{
				Action:  action,
				Matcher: networkv1alpha1.Matcher{Prefix: &networkv1alpha1.PrefixMatcher{Prefix: prefix}},
			})
		}
	}
	if net.Spec.IPv6 != nil && net.Spec.IPv6.CIDR != "" {
		var overrideLen *int32
		if aggCfg != nil {
			overrideLen = aggCfg.PrefixLengthV6
		}
		prefix := computeAggregatePrefix(net.Spec.IPv6.CIDR, overrideLen)
		fvrf.StaticRoutes = appendUniqueStaticRoute(fvrf.StaticRoutes, networkv1alpha1.StaticRoute{
			Prefix:  prefix,
			NextHop: nextHop,
		})
		if fvrf.EVPNExportFilter != nil {
			fvrf.EVPNExportFilter.Items = appendUniqueFilterItem(fvrf.EVPNExportFilter.Items, networkv1alpha1.FilterItem{
				Action:  action,
				Matcher: networkv1alpha1.Matcher{Prefix: &networkv1alpha1.PrefixMatcher{Prefix: prefix}},
			})
		}
	}
}

// appendUniqueFilterItem appends a filter item only if no item with the same prefix exists.
func appendUniqueFilterItem(items []networkv1alpha1.FilterItem, item networkv1alpha1.FilterItem) []networkv1alpha1.FilterItem {
	for _, existing := range items {
		if existing.Matcher.Prefix != nil && item.Matcher.Prefix != nil &&
			existing.Matcher.Prefix.Prefix == item.Matcher.Prefix.Prefix {
			return items
		}
	}
	return append(items, item)
}

// computeAggregatePrefix applies an optional prefix-length override to a Network CIDR.
// The override can only make the aggregate more specific (longer prefix), never broader
// than the original CIDR.
func computeAggregatePrefix(cidr string, overrideLen *int32) string {
	if overrideLen == nil {
		return cidr
	}

	_, ipNet, err := gonet.ParseCIDR(cidr)
	if err != nil {
		return cidr
	}

	ones, bits := ipNet.Mask.Size()
	newLen := int(*overrideLen)

	// Clamp: aggregate can never be broader than the Network CIDR.
	if newLen < ones {
		newLen = ones
	}

	if newLen == ones {
		return cidr
	}

	mask := gonet.CIDRMask(newLen, bits)
	masked := ipNet.IP.Mask(mask)

	return fmt.Sprintf("%s/%d", masked.String(), newLen)
}

// appendUniqueStaticRoute deduplicates identical routes and prefers an explicit
// next hop over a blackhole for the same prefix. Conflicting explicit routes
// remain visible for downstream validation instead of being silently discarded.
func appendUniqueStaticRoute(routes []networkv1alpha1.StaticRoute, route networkv1alpha1.StaticRoute) []networkv1alpha1.StaticRoute {
	for i := range routes {
		if routes[i].Prefix != route.Prefix {
			continue
		}
		switch {
		case sameBuilderStaticRoute(routes[i], route):
			return routes
		case routes[i].NextHop == nil && route.NextHop != nil:
			routes[i] = route
			return routes
		case routes[i].NextHop != nil && route.NextHop == nil:
			return routes
		}
	}
	return append(routes, route)
}

func sameBuilderStaticRoute(a, b networkv1alpha1.StaticRoute) bool {
	if a.Prefix != b.Prefix {
		return false
	}
	if a.NextHop == nil || b.NextHop == nil {
		return a.NextHop == nil && b.NextHop == nil
	}
	return equalBuilderStringPtr(a.NextHop.Address, b.NextHop.Address) &&
		equalBuilderStringPtr(a.NextHop.Vrf, b.NextHop.Vrf)
}

func equalBuilderStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// resolveSelectorVRFs returns ALL VRFs (name → spec) matched by a Destination
// LabelSelector. Used by consumers (Inbound, Outbound, Layer2Attachment) that
// must fan out to every matched VRF rather than picking the first match.
func resolveSelectorVRFs(namespace string, sel *metav1.LabelSelector, data *resolver.ResolvedData) map[string]*nc.VRFSpec {
	if sel == nil {
		return nil
	}
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return nil
	}
	out := map[string]*nc.VRFSpec{}
	for i := range data.RawDestinations {
		rawDest := &data.RawDestinations[i]
		if rawDest.Namespace != namespace || !selector.Matches(labels.Set(rawDest.Labels)) {
			continue
		}
		if rawDest.Spec.VRFRef == nil {
			continue
		}
		resolved, ok := data.Destination(namespace, rawDest.Name)
		if !ok || resolved.VRFSpec == nil {
			continue
		}
		out[resolved.VRFSpec.VRF] = resolved.VRFSpec
	}
	return out
}

// groupDestinationsByVRF resolves a label selector against raw destinations and
// groups ALL matching destinations by their vrfRef. Destinations without vrfRef
// (using nextHop instead) are skipped.
func groupDestinationsByVRF(namespace string, sel *metav1.LabelSelector, data *resolver.ResolvedData) map[string][]nc.Destination {
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return nil
	}

	grouped := make(map[string][]nc.Destination)
	for i := range data.RawDestinations {
		rawDest := &data.RawDestinations[i]
		if rawDest.Namespace != namespace || !selector.Matches(labels.Set(rawDest.Labels)) {
			continue
		}
		if rawDest.Spec.VRFRef == nil {
			continue // nextHop-based destination — no SBR needed
		}
		// Resolve VRFRef → backbone VRF name (spec.vrf) to match the FabricVRF
		// map key convention used by all builders.
		resolved, ok := data.Destination(namespace, rawDest.Name)
		if !ok || resolved.VRFSpec == nil {
			continue
		}
		vrfName := resolved.VRFSpec.VRF
		grouped[vrfName] = append(grouped[vrfName], *rawDest)
	}

	return grouped
}

// mergeFilter merges a new filter into an existing one.
// The base filter's DefaultAction is preserved — only items are appended.
func mergeFilter(existing, addition *networkv1alpha1.Filter) *networkv1alpha1.Filter {
	if existing == nil {
		return addition
	}
	if addition == nil {
		return existing
	}

	merged := *existing
	merged.Items = append(merged.Items, addition.Items...)
	return &merged
}

// mergeRedistribute merges two Redistribute configs, appending items from addition.
func mergeRedistribute(existing, addition *networkv1alpha1.Redistribute) *networkv1alpha1.Redistribute {
	if existing == nil {
		return addition
	}
	if addition == nil {
		return existing
	}

	merged := *existing
	if addition.Connected != nil {
		if merged.Connected == nil {
			merged.Connected = addition.Connected
		} else {
			merged.Connected.Items = append(merged.Connected.Items, addition.Connected.Items...)
		}
	}
	if addition.Static != nil {
		if merged.Static == nil {
			merged.Static = addition.Static
		} else {
			merged.Static.Items = append(merged.Static.Items, addition.Static.Items...)
		}
	}

	return &merged
}
