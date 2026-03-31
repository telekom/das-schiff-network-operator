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

// AnnouncementBuilder transforms AnnouncementPolicy intent CRDs into EVPN export filters.
type AnnouncementBuilder struct{}

// NewAnnouncementBuilder creates a new AnnouncementBuilder.
func NewAnnouncementBuilder() *AnnouncementBuilder {
	return &AnnouncementBuilder{}
}

// Name returns the builder name.
func (b *AnnouncementBuilder) Name() string {
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
		for _, node := range data.Nodes {
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
func (b *AnnouncementBuilder) buildEVPNExportFilter(ap *nc.AnnouncementPolicy) *networkv1alpha1.Filter {
	var items []networkv1alpha1.FilterItem

	// Host routes (/32 /128) community actions.
	if ap.Spec.HostRoutes != nil && len(ap.Spec.HostRoutes.Communities) > 0 {
		additive := true

		// IPv4 host routes (/32).
		ge32 := 32
		items = append(items, networkv1alpha1.FilterItem{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{
					Prefix: "0.0.0.0/0",
					Ge:     &ge32,
					Le:     &ge32,
				},
			},
			Action: networkv1alpha1.Action{
				Type: networkv1alpha1.Accept,
				ModifyRoute: &networkv1alpha1.ModifyRouteAction{
					AddCommunities:  ap.Spec.HostRoutes.Communities,
					AdditiveCommunities: &additive,
				},
			},
		})

		// IPv6 host routes (/128).
		ge128 := 128
		items = append(items, networkv1alpha1.FilterItem{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{
					Prefix: "::/0",
					Ge:     &ge128,
					Le:     &ge128,
				},
			},
			Action: networkv1alpha1.Action{
				Type: networkv1alpha1.Accept,
				ModifyRoute: &networkv1alpha1.ModifyRouteAction{
					AddCommunities:  ap.Spec.HostRoutes.Communities,
					AdditiveCommunities: &additive,
				},
			},
		})
	}

	// Aggregate route config.
	if ap.Spec.Aggregate != nil {
		aggregateEnabled := ap.Spec.Aggregate.Enabled == nil || *ap.Spec.Aggregate.Enabled

		if aggregateEnabled && len(ap.Spec.Aggregate.Communities) > 0 {
			additive := true

			// IPv4 aggregate — match non-host routes.
			le31 := 31
			items = append(items, networkv1alpha1.FilterItem{
				Matcher: networkv1alpha1.Matcher{
					Prefix: &networkv1alpha1.PrefixMatcher{
						Prefix: "0.0.0.0/0",
						Le:     &le31,
					},
				},
				Action: networkv1alpha1.Action{
					Type: networkv1alpha1.Accept,
					ModifyRoute: &networkv1alpha1.ModifyRouteAction{
						AddCommunities:  ap.Spec.Aggregate.Communities,
						AdditiveCommunities: &additive,
					},
				},
			})

			// IPv6 aggregate — match non-host routes.
			le127 := 127
			items = append(items, networkv1alpha1.FilterItem{
				Matcher: networkv1alpha1.Matcher{
					Prefix: &networkv1alpha1.PrefixMatcher{
						Prefix: "::/0",
						Le:     &le127,
					},
				},
				Action: networkv1alpha1.Action{
					Type: networkv1alpha1.Accept,
					ModifyRoute: &networkv1alpha1.ModifyRouteAction{
						AddCommunities:  ap.Spec.Aggregate.Communities,
						AdditiveCommunities: &additive,
					},
				},
			})
		}

		if !aggregateEnabled {
			// Reject aggregate prefixes (non-host routes).
			le31 := 31
			items = append(items, networkv1alpha1.FilterItem{
				Matcher: networkv1alpha1.Matcher{
					Prefix: &networkv1alpha1.PrefixMatcher{
						Prefix: "0.0.0.0/0",
						Le:     &le31,
					},
				},
				Action: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
			})

			le127 := 127
			items = append(items, networkv1alpha1.FilterItem{
				Matcher: networkv1alpha1.Matcher{
					Prefix: &networkv1alpha1.PrefixMatcher{
						Prefix: "::/0",
						Le:     &le127,
					},
				},
				Action: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
			})
		}
	}

	if len(items) == 0 {
		return nil
	}

	return &networkv1alpha1.Filter{
		Items:         items,
		DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
	}
}

// mergeFilter merges a new filter into an existing one.
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
