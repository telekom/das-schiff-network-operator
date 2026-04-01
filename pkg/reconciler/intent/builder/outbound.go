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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// OutboundBuilder transforms Outbound intent CRDs into FabricVRF vrfImport and policy routes for SNAT.
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
func (b *OutboundBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	result := make(map[string]*NodeContribution)

	for i := range data.Outbounds {
		ob := &data.Outbounds[i]

		// Resolve the referenced Network.
		if _, ok := data.Networks[ob.Spec.NetworkRef]; !ok {
			return nil, fmt.Errorf("Outbound %q references unknown Network %q", ob.Name, ob.Spec.NetworkRef)
		}

		// Resolve destinations to find VRF.
		vrfName, vrfSpec, err := b.resolveDestinationVRF(ob, data)
		if err != nil {
			return nil, fmt.Errorf("Outbound %q destination resolution failed: %w", ob.Name, err)
		}

		if vrfName == "" || vrfSpec == nil {
			continue // no VRF plumbing requested
		}

		// Collect allocated addresses for EVPN export and cluster vrfImport filters.
		addresses := b.collectAddresses(ob)

		// Note: SNAT routing is handled by the SBR builder which creates:
		//   ClusterVRF policyRoutes (srcPrefix→s-<vrf>) + LocalVRF static routes (→fabricVRF).
		// The FabricVRF only needs EVPN export + vrfImport for the allocated addresses.

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

			// Add allocated addresses to EVPN export filter and cluster vrfImport.
			// Routing to these IPs happens dynamically via vrfImport from cluster,
			// NOT via static routes.
			filterItems := addressFilterItems(addresses)
			if fvrf.EVPNExportFilter != nil {
				fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, filterItems...)
			}
			if len(fvrf.VRFImports) > 0 {
				fvrf.VRFImports[0].Filter.Items = append(fvrf.VRFImports[0].Filter.Items, filterItems...)
			}

			contrib.FabricVRFs[vrfName] = fvrf
		}
	}

	return result, nil
}

// resolveDestinationVRF finds the VRF for an Outbound by matching its destination selector.
func (b *OutboundBuilder) resolveDestinationVRF(ob *nc.Outbound, data *resolver.ResolvedData) (string, *nc.VRFSpec, error) {
	if ob.Spec.Destinations == nil {
		return "", nil, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(ob.Spec.Destinations)
	if err != nil {
		return "", nil, fmt.Errorf("invalid destination selector: %w", err)
	}

	for i := range data.RawDestinations {
		rawDest := &data.RawDestinations[i]
		if selector.Matches(labels.Set(rawDest.Labels)) {
			resolved, ok := data.Destinations[rawDest.Name]
			if ok && resolved.VRFSpec != nil && resolved.Spec.VRFRef != nil {
				return resolved.VRFSpec.VRF, resolved.VRFSpec, nil
			}
		}
	}

	return "", nil, nil
}

// collectAddresses gathers explicit addresses from the Outbound spec.
func (b *OutboundBuilder) collectAddresses(ob *nc.Outbound) []string {
	if ob.Spec.Addresses == nil {
		return nil
	}
	var addrs []string
	addrs = append(addrs, ob.Spec.Addresses.IPv4...)
	addrs = append(addrs, ob.Spec.Addresses.IPv6...)
	return addrs
}
