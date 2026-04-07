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

// AnnouncementBuilder transforms AnnouncementPolicy intent CRDs into EVPN export filters.
type AnnouncementBuilder struct{}

// NewAnnouncementBuilder creates a new AnnouncementBuilder.
func NewAnnouncementBuilder() *AnnouncementBuilder {
	return &AnnouncementBuilder{}
}

// Name returns the builder name.
func (*AnnouncementBuilder) Name() string {
	return "announcement"
}

// Build produces per-node EVPN export filter contributions from AnnouncementPolicy resources.
func (b *AnnouncementBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	result := make(map[string]*NodeContribution)

	for i := range data.AnnouncementPolicies {
		ap := &data.AnnouncementPolicies[i]

		// Resolve VRF reference.
		resolvedVRF, ok := data.VRFs[ap.Spec.VRFRef]
		if !ok {
			return nil, fmt.Errorf("AnnouncementPolicy %q references unknown VRF %q", ap.Name, ap.Spec.VRFRef)
		}

		// Build EVPN export filter.
		filter := b.buildEVPNExportFilter(ap)

		// Apply to all nodes where VRF exists.
		for i := range data.Nodes {
			node := &data.Nodes[i]
			contrib, ok := result[node.Name]
			if !ok {
				contrib = NewNodeContribution()
				result[node.Name] = contrib
			}

			vrfName := ap.Spec.VRFRef
			fvrf, exists := contrib.FabricVRFs[vrfName]
			if !exists {
				fvrf = buildFabricVRF(&resolvedVRF.Spec)
			}

			fvrf.EVPNExportFilter = mergeFilter(fvrf.EVPNExportFilter, filter)
			contrib.FabricVRFs[vrfName] = fvrf
		}
	}

	return result, nil
}

// buildEVPNExportFilter constructs an EVPN export filter from an AnnouncementPolicy.
func (*AnnouncementBuilder) buildEVPNExportFilter(ap *nc.AnnouncementPolicy) *networkv1alpha1.Filter {
	var items []networkv1alpha1.FilterItem

	items = append(items, buildHostRouteFilterItems(ap.Spec.HostRoutes)...)
	items = append(items, buildAggregateFilterItems(ap.Spec.Aggregate)...)

	if len(items) == 0 {
		return nil
	}

	return &networkv1alpha1.Filter{
		Items:         items,
		DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
	}
}

// buildHostRouteFilterItems creates filter items for host route announcements (/32 and /128).
func buildHostRouteFilterItems(cfg *nc.RouteAnnouncementConfig) []networkv1alpha1.FilterItem {
	if cfg == nil || len(cfg.Communities) == 0 {
		return nil
	}

	additive := true
	ge32 := ipv4MaxPrefixLen
	ge128 := ipv6MaxPrefixLen

	return []networkv1alpha1.FilterItem{
		{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: "0.0.0.0/0", Ge: &ge32, Le: &ge32},
			},
			Action: networkv1alpha1.Action{
				Type: networkv1alpha1.Accept,
				ModifyRoute: &networkv1alpha1.ModifyRouteAction{
					AddCommunities:      cfg.Communities,
					AdditiveCommunities: &additive,
				},
			},
		},
		{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: "::/0", Ge: &ge128, Le: &ge128},
			},
			Action: networkv1alpha1.Action{
				Type: networkv1alpha1.Accept,
				ModifyRoute: &networkv1alpha1.ModifyRouteAction{
					AddCommunities:      cfg.Communities,
					AdditiveCommunities: &additive,
				},
			},
		},
	}
}

// buildAggregateFilterItems creates filter items for aggregate route announcements.
func buildAggregateFilterItems(cfg *nc.AggregateConfig) []networkv1alpha1.FilterItem {
	if cfg == nil {
		return nil
	}

	aggregateEnabled := cfg.Enabled == nil || *cfg.Enabled
	le31 := ipv4HostRouteLen
	le127 := ipv6HostRouteLen

	if aggregateEnabled && len(cfg.Communities) > 0 {
		additive := true
		return []networkv1alpha1.FilterItem{
			{
				Matcher: networkv1alpha1.Matcher{
					Prefix: &networkv1alpha1.PrefixMatcher{Prefix: "0.0.0.0/0", Le: &le31},
				},
				Action: networkv1alpha1.Action{
					Type: networkv1alpha1.Accept,
					ModifyRoute: &networkv1alpha1.ModifyRouteAction{
						AddCommunities:      cfg.Communities,
						AdditiveCommunities: &additive,
					},
				},
			},
			{
				Matcher: networkv1alpha1.Matcher{
					Prefix: &networkv1alpha1.PrefixMatcher{Prefix: "::/0", Le: &le127},
				},
				Action: networkv1alpha1.Action{
					Type: networkv1alpha1.Accept,
					ModifyRoute: &networkv1alpha1.ModifyRouteAction{
						AddCommunities:      cfg.Communities,
						AdditiveCommunities: &additive,
					},
				},
			},
		}
	}

	if !aggregateEnabled {
		return []networkv1alpha1.FilterItem{
			{
				Matcher: networkv1alpha1.Matcher{
					Prefix: &networkv1alpha1.PrefixMatcher{Prefix: "0.0.0.0/0", Le: &le31},
				},
				Action: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
			},
			{
				Matcher: networkv1alpha1.Matcher{
					Prefix: &networkv1alpha1.PrefixMatcher{Prefix: "::/0", Le: &le127},
				},
				Action: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
			},
		}
	}

	return nil
}

// mergeFilter merges a new filter into an existing one.
// The base filter's DefaultAction is preserved — only items are appended.
// This keeps deny-by-default EVPN export semantics: only explicitly matched
// routes are exported, regardless of the addition's default action.
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
