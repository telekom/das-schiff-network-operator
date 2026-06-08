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

// OutboundBuilder transforms Outbound intent CRDs into FabricVRF vrfImport config.
type OutboundBuilder struct{}

// NewOutboundBuilder creates a new OutboundBuilder.
func NewOutboundBuilder() *OutboundBuilder {
	return &OutboundBuilder{}
}

// Name returns the builder name.
func (*OutboundBuilder) Name() string {
	return "outbound"
}

// Build produces per-node FabricVRF contributions from Outbound resources.
// A single Outbound may select multiple Destinations across different VRFs
// via its label selector — FabricVRF entries are produced for each matched VRF.
func (b *OutboundBuilder) Build(ctx context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	logger := log.FromContext(ctx).WithName("outbound-builder")
	result := make(map[string]*NodeContribution)

	for i := range data.Outbounds {
		ob := &data.Outbounds[i]

		net, ok := data.Networks[ob.Spec.NetworkRef]
		if !ok {
			continue
		}

		if ob.Spec.Destinations == nil {
			continue
		}

		if err := b.applyOutbound(ob, net, data, result); err != nil {
			logger.Info("skipping Outbound with ambiguous announcement policy",
				"outbound", ob.Name, "error", err.Error())
			reportSkip(ctx, "Outbound", ob.Name, "AmbiguousAnnouncementPolicy", err.Error())
			continue
		}
	}

	return result, nil
}

// outboundVRFContrib holds the pre-validated per-VRF data for one Outbound.
type outboundVRFContrib struct {
	vrfName    string
	vrfSpec    *nc.VRFSpec
	ap         *nc.AnnouncementPolicy
	evpnItems  []networkv1alpha1.FilterItem
	plainItems []networkv1alpha1.FilterItem
}

// applyOutbound fans a single Outbound across its matched VRFs and all nodes.
// All AnnouncementPolicy resolution is performed up front so a validation error
// (e.g. ambiguous policy) skips the whole Outbound without leaving a partially
// applied VRF behind.
func (b *OutboundBuilder) applyOutbound(
	ob *nc.Outbound,
	net *resolver.ResolvedNetwork,
	data *resolver.ResolvedData,
	result map[string]*NodeContribution,
) error {
	grouped := groupDestinationsByVRF(ob.Spec.Destinations, data)
	if len(grouped) == 0 {
		return nil
	}

	addresses := b.collectAddresses(ob)

	// Validate and resolve every VRF before mutating any node contribution.
	contribs := make([]outboundVRFContrib, 0, len(grouped))
	for vrfName, dests := range grouped {
		ap, err := findMatchingAP(ob.Labels, vrfName, data)
		if err != nil {
			return fmt.Errorf("outbound %q: %w", ob.Name, err)
		}

		vrfSpec := b.resolveVRFSpec(dests, data)
		if vrfSpec == nil {
			continue
		}

		contribs = append(contribs, outboundVRFContrib{
			vrfName:    vrfName,
			vrfSpec:    vrfSpec,
			ap:         ap,
			evpnItems:  addressFilterItems(addresses, ap),
			plainItems: addressFilterItems(addresses, nil),
		})
	}

	for ci := range contribs {
		c := &contribs[ci]
		for i := range data.Nodes {
			contrib := ensureContrib(result, data.Nodes[i].Name)

			fvrf, exists := contrib.FabricVRFs[c.vrfName]
			if !exists {
				fvrf = buildFabricVRF(c.vrfSpec)
			}

			if fvrf.EVPNExportFilter != nil {
				fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, c.evpnItems...)
			}
			if len(fvrf.VRFImports) > 0 {
				fvrf.VRFImports[0].Filter.Items = append(fvrf.VRFImports[0].Filter.Items, c.plainItems...)
			}

			addAggregateRoutes(&fvrf, net, c.ap)
			contrib.FabricVRFs[c.vrfName] = fvrf
		}
	}

	return nil
}

// resolveVRFSpec finds the VRFSpec from a set of destinations for the same VRF.
func (*OutboundBuilder) resolveVRFSpec(dests []nc.Destination, data *resolver.ResolvedData) *nc.VRFSpec {
	if len(dests) == 0 {
		return nil
	}
	resolved, ok := data.Destinations[dests[0].Name]
	if !ok || resolved.VRFSpec == nil {
		return nil
	}
	return resolved.VRFSpec
}

// collectAddresses gathers addresses from the Outbound.
// It prefers status.addresses (IPAM-allocated) over spec.addresses (explicit).
func (*OutboundBuilder) collectAddresses(ob *nc.Outbound) []string {
	src := ob.Status.Addresses
	if src == nil {
		src = ob.Spec.Addresses
	}
	if src == nil {
		return nil
	}
	addrs := make([]string, 0, len(src.IPv4)+len(src.IPv6))
	addrs = append(addrs, src.IPv4...)
	addrs = append(addrs, src.IPv6...)
	return addrs
}
