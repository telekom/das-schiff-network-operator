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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// PodNetworkBuilder transforms PodNetwork intent CRDs into FabricVRF CNI routing config.
type PodNetworkBuilder struct{}

// NewPodNetworkBuilder creates a new PodNetworkBuilder.
func NewPodNetworkBuilder() *PodNetworkBuilder {
	return &PodNetworkBuilder{}
}

// Name returns the builder name.
func (*PodNetworkBuilder) Name() string {
	return "podnetwork"
}

// Build produces per-node FabricVRF contributions from PodNetwork resources.
func (b *PodNetworkBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	result := make(map[string]*NodeContribution)

	for i := range data.PodNetworks {
		pn := &data.PodNetworks[i]

		// Resolve the referenced Network.
		net, ok := data.Networks[pn.Spec.NetworkRef]
		if !ok {
			return nil, fmt.Errorf("PodNetwork %q references unknown Network %q", pn.Name, pn.Spec.NetworkRef)
		}

		// Resolve destinations to find VRF.
		vrfName, vrfSpec, err := b.resolveDestinationVRF(pn, data)
		if err != nil {
			return nil, fmt.Errorf("PodNetwork %q destination resolution failed: %w", pn.Name, err)
		}

		if vrfName == "" || vrfSpec == nil {
			continue // no VRF plumbing requested
		}

		// Build redistribute connected filter for pod CIDR.
		redistribute := b.buildRedistribute(net)

		// Collect extra static routes from Routes field.
		staticRoutes := b.buildExtraRoutes(pn)

		// PodNetwork applies to all nodes (no nodeSelector).
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
			fvrf.StaticRoutes = append(fvrf.StaticRoutes, staticRoutes...)

			contrib.FabricVRFs[vrfName] = fvrf
		}
	}

	return result, nil
}

// resolveDestinationVRF finds the VRF for a PodNetwork by matching its destination selector.
func (*PodNetworkBuilder) resolveDestinationVRF(pn *nc.PodNetwork, data *resolver.ResolvedData) (string, *nc.VRFSpec, error) {
	if pn.Spec.Destinations == nil {
		return "", nil, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(pn.Spec.Destinations)
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

// buildRedistribute creates a redistribute connected filter for the PodNetwork CIDRs.
func (*PodNetworkBuilder) buildRedistribute(net *resolver.ResolvedNetwork) *networkv1alpha1.Redistribute {
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

// buildExtraRoutes creates static routes from the PodNetwork's Routes field.
func (*PodNetworkBuilder) buildExtraRoutes(pn *nc.PodNetwork) []networkv1alpha1.StaticRoute {
	totalPrefixes := 0
	for _, r := range pn.Spec.Routes {
		totalPrefixes += len(r.Prefixes)
	}
	routes := make([]networkv1alpha1.StaticRoute, 0, totalPrefixes)
	for _, r := range pn.Spec.Routes {
		for _, prefix := range r.Prefixes {
			routes = append(routes, networkv1alpha1.StaticRoute{
				Prefix: prefix,
			})
		}
	}
	return routes
}
