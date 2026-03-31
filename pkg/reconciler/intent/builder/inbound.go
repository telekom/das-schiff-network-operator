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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// InboundBuilder transforms Inbound intent CRDs into FabricVRF static routes and redistribute config.
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
func (b *InboundBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	result := make(map[string]*NodeContribution)

	for i := range data.Inbounds {
		ib := &data.Inbounds[i]

		// Resolve the referenced Network.
		net, ok := data.Networks[ib.Spec.NetworkRef]
		if !ok {
			return nil, fmt.Errorf("Inbound %q references unknown Network %q", ib.Name, ib.Spec.NetworkRef)
		}

		// Resolve destinations to find VRF.
		vrfName, vrfSpec, err := b.resolveDestinationVRF(ib, data)
		if err != nil {
			return nil, fmt.Errorf("Inbound %q destination resolution failed: %w", ib.Name, err)
		}

		if vrfName == "" || vrfSpec == nil {
			continue // no VRF plumbing requested
		}

		// Collect allocated addresses for static routes.
		addresses := b.collectAddresses(ib)

		// Build static routes from explicit addresses.
		var staticRoutes []networkv1alpha1.StaticRoute
		for _, addr := range addresses {
			staticRoutes = append(staticRoutes, networkv1alpha1.StaticRoute{
				Prefix: addr,
			})
		}

		// Build redistribute connected filter for Inbound CIDRs.
		redistribute := b.buildRedistribute(net)

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

			fvrf.StaticRoutes = append(fvrf.StaticRoutes, staticRoutes...)
			if redistribute != nil {
				fvrf.Redistribute = mergeRedistribute(fvrf.Redistribute, redistribute)
			}

			contrib.FabricVRFs[vrfName] = fvrf
		}
	}

	return result, nil
}

// resolveDestinationVRF finds the VRF for an Inbound by matching its destination selector.
func (b *InboundBuilder) resolveDestinationVRF(ib *nc.Inbound, data *resolver.ResolvedData) (string, *nc.VRFSpec, error) {
	if ib.Spec.Destinations == nil {
		return "", nil, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(ib.Spec.Destinations)
	if err != nil {
		return "", nil, fmt.Errorf("invalid destination selector: %w", err)
	}

	for i := range data.RawDestinations {
		rawDest := &data.RawDestinations[i]
		if selector.Matches(labels.Set(rawDest.Labels)) {
			resolved, ok := data.Destinations[rawDest.Name]
			if ok && resolved.VRFSpec != nil && resolved.Spec.VRFRef != nil {
				return *resolved.Spec.VRFRef, resolved.VRFSpec, nil
			}
		}
	}

	return "", nil, nil
}

// collectAddresses gathers explicit addresses from the Inbound spec.
func (b *InboundBuilder) collectAddresses(ib *nc.Inbound) []string {
	if ib.Spec.Addresses == nil {
		return nil
	}
	var addrs []string
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
