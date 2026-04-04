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

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// OutboundBuilder transforms Outbound intent CRDs into FabricVRF vrfImport config.
type OutboundBuilder struct{}

// NewOutboundBuilder creates a new OutboundBuilder.
func NewOutboundBuilder() *OutboundBuilder {
	return &OutboundBuilder{}
}

// Name returns the builder name.
func (b *OutboundBuilder) Name() string {
	return "outbound"
}

// Build produces per-node FabricVRF contributions from Outbound resources.
// A single Outbound may select multiple Destinations across different VRFs
// via its label selector — FabricVRF entries are produced for each matched VRF.
func (b *OutboundBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	result := make(map[string]*NodeContribution)

	for i := range data.Outbounds {
		ob := &data.Outbounds[i]

		// Resolve the referenced Network.
		if _, ok := data.Networks[ob.Spec.NetworkRef]; !ok {
			return nil, fmt.Errorf("Outbound %q references unknown Network %q", ob.Name, ob.Spec.NetworkRef)
		}

		if ob.Spec.Destinations == nil {
			continue
		}

		// Resolve ALL matching destinations, grouped by VRF.
		grouped := groupDestinationsByVRF(ob.Spec.Destinations, data)
		if len(grouped) == 0 {
			continue
		}

		// Collect allocated addresses for EVPN export and cluster vrfImport filters.
		addresses := b.collectAddresses(ob)
		filterItems := addressFilterItems(addresses)

		// SNAT routing chain is handled by the SBR builder:
		//   ClusterVRF policyRoutes (src→s-<vrf>) + LocalVRF static routes (→fabricVRF).
		// The FabricVRF only needs EVPN export + vrfImport for the allocated addresses.

		// Produce FabricVRF contributions for each matched VRF.
		for vrfName, dests := range grouped {
			vrfSpec := b.resolveVRFSpec(dests, data)
			if vrfSpec == nil {
				continue
			}

			// Outbound applies to all nodes (no nodeSelector).
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

// resolveVRFSpec finds the VRFSpec from a set of destinations for the same VRF.
func (b *OutboundBuilder) resolveVRFSpec(dests []nc.Destination, data *resolver.ResolvedData) *nc.VRFSpec {
	if len(dests) == 0 {
		return nil
	}
	resolved, ok := data.Destinations[dests[0].Name]
	if !ok || resolved.VRFSpec == nil {
		return nil
	}
	return resolved.VRFSpec
}

// collectAddresses gathers explicit addresses from the Outbound spec.
func (b *OutboundBuilder) collectAddresses(ob *nc.Outbound) []string {
	if ob.Spec.Addresses == nil {
		return nil
	}
	addrs := make([]string, 0, len(ob.Spec.Addresses.IPv4)+len(ob.Spec.Addresses.IPv6))
	addrs = append(addrs, ob.Spec.Addresses.IPv4...)
	addrs = append(addrs, ob.Spec.Addresses.IPv6...)
	return addrs
}
