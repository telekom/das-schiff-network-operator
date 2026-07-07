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

	"sigs.k8s.io/controller-runtime/pkg/log"

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
func (*InboundBuilder) Name() string {
	return "inbound"
}

// Build produces per-node FabricVRF contributions from Inbound resources.
// A single Inbound may select multiple Destinations across different VRFs
// via its label selector — FabricVRF entries are produced for each matched VRF.
func (b *InboundBuilder) Build(ctx context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	logger := log.FromContext(ctx).WithName("inbound-builder")
	result := make(map[string]*NodeContribution)

	for i := range data.Inbounds {
		ib := &data.Inbounds[i]

		// Resolve the referenced Network.
		net, ok := data.Networks[ib.Spec.NetworkRef]
		if !ok {
			logger.Info("skipping Inbound with unknown Network reference",
				"inbound", ib.Name, "networkRef", ib.Spec.NetworkRef)
			reportSkip(ctx, "Inbound", ib.Namespace, ib.Name, "NetworkNotFound",
				fmt.Sprintf("referenced Network %q not found", ib.Spec.NetworkRef))
			continue
		}

		if ib.Spec.Destinations == nil {
			continue
		}

		grouped := groupDestinationsByVRF(ib.Spec.Destinations, data)
		if len(grouped) == 0 {
			continue
		}

		// Validate AnnouncementPolicy resolution for every matched VRF up front so
		// an ambiguous policy skips the whole Inbound rather than leaving a
		// partially-applied VRF behind.
		aps := make(map[string]*nc.AnnouncementPolicy, len(grouped))
		ambiguous := false
		for vrfName := range grouped {
			ap, err := findMatchingAP(ib.Labels, vrfName, data)
			if err != nil {
				logger.Info("skipping Inbound with ambiguous announcement policy",
					"inbound", ib.Name, "error", err.Error())
				reportSkip(ctx, "Inbound", ib.Namespace, ib.Name, "AmbiguousAnnouncementPolicy", err.Error())
				ambiguous = true
				break
			}
			aps[vrfName] = ap
		}
		if ambiguous {
			continue
		}

		// Collect allocated addresses for EVPN export and cluster vrfImport filters.
		addresses := b.collectAddresses(ib)

		// Build redistribute connected filter for Inbound CIDRs.
		redistribute := b.buildRedistribute(net)

		// Produce FabricVRF contributions for each matched VRF.
		for vrfName := range grouped {
			vrfSpec := b.resolveVRFSpec(vrfName, grouped, data)
			if vrfSpec == nil {
				continue
			}
			b.applyInboundToNodes(vrfName, vrfSpec, addresses, redistribute, net, data, result, aps[vrfName])
		}
	}

	return result, nil
}

// applyInboundToNodes applies inbound FabricVRF config to all nodes for a single VRF.
func (*InboundBuilder) applyInboundToNodes(
	vrfName string,
	vrfSpec *nc.VRFSpec,
	addresses []string,
	redistribute *networkv1alpha1.Redistribute,
	net *resolver.ResolvedNetwork,
	data *resolver.ResolvedData,
	result map[string]*NodeContribution,
	ap *nc.AnnouncementPolicy,
) {
	evpnItems := addressFilterItems(addresses, ap)
	plainItems := addressFilterItems(addresses, nil)

	for i := range data.Nodes {
		contrib, ok := result[data.Nodes[i].Name]
		if !ok {
			contrib = NewNodeContribution()
			result[data.Nodes[i].Name] = contrib
		}

		fvrf, exists := contrib.FabricVRFs[vrfName]
		if !exists {
			fvrf = buildFabricVRF(vrfSpec)
		}

		if redistribute != nil {
			fvrf.Redistribute = mergeRedistribute(fvrf.Redistribute, redistribute)
		}

		if fvrf.EVPNExportFilter != nil {
			fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, evpnItems...)
		}
		if len(fvrf.VRFImports) > 0 {
			fvrf.VRFImports[0].Filter.Items = append(fvrf.VRFImports[0].Filter.Items, plainItems...)
		}

		addAggregateRoutes(&fvrf, net, ap)
		contrib.FabricVRFs[vrfName] = fvrf
	}
}

// resolveVRFSpec finds the VRFSpec for a given VRF name from the grouped destinations.
func (*InboundBuilder) resolveVRFSpec(vrfName string, grouped map[string][]nc.Destination, data *resolver.ResolvedData) *nc.VRFSpec {
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

// collectAddresses gathers addresses from the Inbound.
// It prefers status.addresses (IPAM-allocated) over spec.addresses (explicit).
func (*InboundBuilder) collectAddresses(ib *nc.Inbound) []string {
	src := ib.Status.Addresses
	if src == nil {
		src = ib.Spec.Addresses
	}
	if src == nil {
		return nil
	}
	addrs := make([]string, 0, len(src.IPv4)+len(src.IPv6))
	addrs = append(addrs, src.IPv4...)
	addrs = append(addrs, src.IPv6...)
	return addrs
}

// buildRedistribute creates a redistribute connected filter for the Inbound Network CIDRs.
func (*InboundBuilder) buildRedistribute(net *resolver.ResolvedNetwork) *networkv1alpha1.Redistribute {
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
