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
					FromVRF: "cluster",
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
func findMatchingAP(usageCRDLabels map[string]string, vrfName string, data *resolver.ResolvedData) (*nc.AnnouncementPolicy, error) {
	var matches []*nc.AnnouncementPolicy

	for i := range data.AnnouncementPolicies {
		ap := &data.AnnouncementPolicies[i]

		resolved, ok := data.VRFs[ap.Spec.VRFRef]
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
	evpnItems := networkCIDRFilterItems(net, ap)
	if len(evpnItems) == 0 {
		return *fvrf
	}

	// Add to EVPN export filter (with AP communities).
	if fvrf.EVPNExportFilter == nil {
		fvrf.EVPNExportFilter = &networkv1alpha1.Filter{
			DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		}
	}
	fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, evpnItems...)

	// Add to cluster VRFImport filter (plain, no AP communities).
	plainItems := networkCIDRFilterItems(net, nil)
	if len(fvrf.VRFImports) > 0 {
		fvrf.VRFImports[0].Filter.Items = append(fvrf.VRFImports[0].Filter.Items, plainItems...)
	}

	return *fvrf
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
		le := 32
		if strings.Contains(addr, ":") {
			le = 128
		}
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
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: addr, Le: &le},
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

// addAggregateRoutes adds the Network CIDR(s) as aggregate static routes to the FabricVRF.
// By default, the covering prefix is always added so the fabric can export it via EVPN.
// When an AP is provided its aggregate config controls prefix length and suppression.
func addAggregateRoutes(fvrf *networkv1alpha1.FabricVRF, net *resolver.ResolvedNetwork, ap *nc.AnnouncementPolicy) {
	if ap != nil && ap.Spec.Aggregate != nil && ap.Spec.Aggregate.Enabled != nil && !*ap.Spec.Aggregate.Enabled {
		return
	}

	var aggCfg *nc.AggregateConfig
	if ap != nil {
		aggCfg = ap.Spec.Aggregate
	}

	if net.Spec.IPv4 != nil && net.Spec.IPv4.CIDR != "" {
		var overrideLen *int32
		if aggCfg != nil {
			overrideLen = aggCfg.PrefixLengthV4
		}
		prefix := computeAggregatePrefix(net.Spec.IPv4.CIDR, overrideLen)
		fvrf.StaticRoutes = appendUniqueStaticRoute(fvrf.StaticRoutes, networkv1alpha1.StaticRoute{
			Prefix: prefix,
		})
	}
	if net.Spec.IPv6 != nil && net.Spec.IPv6.CIDR != "" {
		var overrideLen *int32
		if aggCfg != nil {
			overrideLen = aggCfg.PrefixLengthV6
		}
		prefix := computeAggregatePrefix(net.Spec.IPv6.CIDR, overrideLen)
		fvrf.StaticRoutes = appendUniqueStaticRoute(fvrf.StaticRoutes, networkv1alpha1.StaticRoute{
			Prefix: prefix,
		})
	}
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

// appendUniqueStaticRoute appends a static route only if no route with the same prefix exists.
func appendUniqueStaticRoute(routes []networkv1alpha1.StaticRoute, route networkv1alpha1.StaticRoute) []networkv1alpha1.StaticRoute {
	for _, r := range routes {
		if r.Prefix == route.Prefix {
			return routes
		}
	}
	return append(routes, route)
}

// groupDestinationsByVRF resolves a label selector against raw destinations and
// groups ALL matching destinations by their vrfRef. Destinations without vrfRef
// (using nextHop instead) are skipped.
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
		// Resolve VRFRef → backbone VRF name (spec.vrf) to match the FabricVRF
		// map key convention used by all builders.
		resolved, ok := data.Destinations[rawDest.Name]
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
