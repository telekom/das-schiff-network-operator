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

// InboundBuilder transforms Inbound intent CRDs into FabricVRF vrfImport and redistribute config.
type InboundBuilder struct{}

// NewInboundBuilder creates a new InboundBuilder.
func NewInboundBuilder() *InboundBuilder {
	return &InboundBuilder{}
}

// Name returns the builder name.
func (b *InboundBuilder) Name() string {
	return "inbound"
}

// Build produces per-node FabricVRF contributions from Inbound resources.
// A single Inbound may select multiple Destinations across different VRFs
// via its label selector — FabricVRF entries are produced for each matched VRF.
func (b *InboundBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) { //nolint:gocognit // inbound building has many valid branches
	result := make(map[string]*NodeContribution)

	for i := range data.Inbounds {
		ib := &data.Inbounds[i]

		// Resolve the referenced Network.
		net, ok := data.Networks[ib.Spec.NetworkRef]
		if !ok {
			return nil, fmt.Errorf("Inbound %q references unknown Network %q", ib.Name, ib.Spec.NetworkRef)
		}

		if ib.Spec.Destinations == nil {
			continue
		}

		// Resolve ALL matching destinations, grouped by VRF.
		grouped := groupDestinationsByVRF(ib.Spec.Destinations, data)
		if len(grouped) == 0 {
			continue
		}

		// Collect allocated addresses for EVPN export and cluster vrfImport filters.
		addresses := b.collectAddresses(ib)
		filterItems := addressFilterItems(addresses)

		// Build redistribute connected filter for Inbound CIDRs.
		redistribute := b.buildRedistribute(net)

		// Produce FabricVRF contributions for each matched VRF.
		for vrfName, _ := range grouped {
			vrfSpec := b.resolveVRFSpec(vrfName, grouped, data)
			if vrfSpec == nil {
				continue
			}

			// Inbound applies to all nodes (no nodeSelector).
			for _, node := range data.Nodes {
				contrib, ok := result[node.Name]
				if !ok {
					contrib = NewNodeContribution()
					result[node.Name] = contrib
				}

				fvrf, exists := contrib.FabricVRFs[vrfName]
				if !exists {
					fvrf = buildFabricVRF(vrfSpec)
				}

				if redistribute != nil {
					fvrf.Redistribute = mergeRedistribute(fvrf.Redistribute, redistribute)
				}

				// Add allocated addresses to EVPN export filter and cluster vrfImport.
				if fvrf.EVPNExportFilter != nil {
					fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, filterItems...)
				}
				if len(fvrf.VRFImports) > 0 {
					fvrf.VRFImports[0].Filter.Items = append(fvrf.VRFImports[0].Filter.Items, filterItems...)
				}

				contrib.FabricVRFs[vrfName] = fvrf
			}
		}
	}

	return result, nil
}

// resolveVRFSpec finds the VRFSpec for a given VRF name from the grouped destinations.
func (b *InboundBuilder) resolveVRFSpec(vrfName string, grouped map[string][]nc.Destination, data *resolver.ResolvedData) *nc.VRFSpec {
	dests := grouped[vrfName]
	if len(dests) == 0 {
		return nil
	}
	resolved, ok := data.Destinations[dests[0].Name]
	if !ok || resolved.VRFSpec == nil {
		return nil
	}
	return resolved.VRFSpec
}

// collectAddresses gathers explicit addresses from the Inbound spec.
func (b *InboundBuilder) collectAddresses(ib *nc.Inbound) []string {
	if ib.Spec.Addresses == nil {
		return nil
	}
	addrs := make([]string, 0, len(ib.Spec.Addresses.IPv4)+len(ib.Spec.Addresses.IPv6))
	addrs = append(addrs, ib.Spec.Addresses.IPv4...)
	addrs = append(addrs, ib.Spec.Addresses.IPv6...)
	return addrs
}

// buildRedistribute creates a redistribute connected filter for the Inbound Network CIDRs.
func (b *InboundBuilder) buildRedistribute(net *resolver.ResolvedNetwork) *networkv1alpha1.Redistribute {
	var items []networkv1alpha1.FilterItem

	if net.Spec.IPv4 != nil {
		items = append(items, networkv1alpha1.FilterItem{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{
					Prefix: net.Spec.IPv4.CIDR,
				},
			},
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
		})
	}

	if net.Spec.IPv6 != nil {
		items = append(items, networkv1alpha1.FilterItem{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{
					Prefix: net.Spec.IPv6.CIDR,
				},
			},
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
		})
	}

	if len(items) == 0 {
		return nil
	}

	return &networkv1alpha1.Redistribute{
		Connected: &networkv1alpha1.Filter{
			Items:         items,
			DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		},
	}
}

// mergeRedistribute merges a new redistribute config into an existing one.
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
