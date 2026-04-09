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

// BGPPeeringBuilder transforms BGPPeering intent CRDs into VRF BGPPeer configurations.
type BGPPeeringBuilder struct{}

// NewBGPPeeringBuilder creates a new BGPPeeringBuilder.
func NewBGPPeeringBuilder() *BGPPeeringBuilder {
	return &BGPPeeringBuilder{}
}

// Name returns the builder name.
func (*BGPPeeringBuilder) Name() string {
	return "bgppeering"
}

// Build produces per-node BGPPeer contributions from BGPPeering resources.
func (b *BGPPeeringBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	result := make(map[string]*NodeContribution)

	for i := range data.BGPPeerings {
		bp := &data.BGPPeerings[i]

		switch bp.Spec.Mode {
		case nc.BGPPeeringModeListenRange:
			if err := b.buildListenRange(bp, data, result); err != nil {
				return nil, fmt.Errorf("BGPPeering %q (listenRange) failed: %w", bp.Name, err)
			}
		case nc.BGPPeeringModeLoopbackPeer:
			b.buildLoopbackPeer(bp, data, result)
		default:
			return nil, fmt.Errorf("BGPPeering %q has unknown mode %q", bp.Name, bp.Spec.Mode)
		}
	}

	return result, nil
}

// buildListenRange creates BGPPeer entries with ListenRange on the IRB VRF.
func (b *BGPPeeringBuilder) buildListenRange(bp *nc.BGPPeering, data *resolver.ResolvedData, result map[string]*NodeContribution) error {
	if bp.Spec.Ref.AttachmentRef == nil {
		return fmt.Errorf("listenRange mode requires attachmentRef")
	}

	// Look up the L2A by name.
	var l2a *nc.Layer2Attachment
	for j := range data.Layer2Attachments {
		if data.Layer2Attachments[j].Name == *bp.Spec.Ref.AttachmentRef {
			l2a = &data.Layer2Attachments[j]
			break
		}
	}
	if l2a == nil {
		return fmt.Errorf("attachmentRef %q not found", *bp.Spec.Ref.AttachmentRef)
	}

	// Resolve the L2A's Network to get the CIDR for listen range.
	net, ok := data.Networks[l2a.Spec.NetworkRef]
	if !ok {
		return fmt.Errorf("Layer2Attachment %q references unknown Network %q", l2a.Name, l2a.Spec.NetworkRef)
	}

	// Resolve the L2A's destination VRF for the IRB.
	vrfName, vrfSpec := b.resolveL2AVRF(l2a, data)
	if vrfName == "" || vrfSpec == nil {
		return fmt.Errorf("Layer2Attachment %q has no VRF for IRB", l2a.Name)
	}

	// Resolve inboundRefs to get the addresses the workload advertises.
	inboundIPv4, inboundIPv6 := b.resolveInboundAddresses(bp, data)

	// Build BGPPeer with ListenRange, import filter from Inbound addresses,
	// and EVPN export items for those same addresses.
	peers := b.buildListenRangePeers(bp, net, inboundIPv4, inboundIPv6)
	evpnExportItems := b.inboundEVPNExportItems(inboundIPv4, inboundIPv6)

	// Apply to all nodes (no nodeSelector on BGPPeering).
	for i := range data.Nodes {
		node := &data.Nodes[i]
		contrib, ok := result[node.Name]
		if !ok {
			contrib = NewNodeContribution()
			result[node.Name] = contrib
		}

		fvrf, exists := contrib.FabricVRFs[vrfName]
		if !exists {
			fvrf = buildFabricVRF(vrfSpec)
		}

		fvrf.BGPPeers = append(fvrf.BGPPeers, peers...)
		// Add Inbound addresses to EVPN export so the fabric distributes them.
		if fvrf.EVPNExportFilter == nil {
			fvrf.EVPNExportFilter = &networkv1alpha1.Filter{
				DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
			}
		}
		fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, evpnExportItems...)
		contrib.FabricVRFs[vrfName] = fvrf
	}

	return nil
}

// buildLoopbackPeer creates BGPPeer entries with Address on the ClusterVRF.
func (b *BGPPeeringBuilder) buildLoopbackPeer(bp *nc.BGPPeering, data *resolver.ResolvedData, result map[string]*NodeContribution) {
	peer := b.buildBasePeer(bp)
	// Loopback peer address is TBD — set to nil (auto-generated ULA by agent).

	// Build address families.
	peer.IPv4, peer.IPv6 = b.buildAddressFamilies(bp)

	// Apply to all nodes.
	for i := range data.Nodes {
		contrib, ok := result[data.Nodes[i].Name]
		if !ok {
			contrib = NewNodeContribution()
			result[data.Nodes[i].Name] = contrib
		}

		if contrib.ClusterVRF == nil {
			contrib.ClusterVRF = &networkv1alpha1.VRF{}
		}

		contrib.ClusterVRF.BGPPeers = append(contrib.ClusterVRF.BGPPeers, peer)
	}
}

// resolveL2AVRF looks up a Layer2Attachment's destination VRF.
func (*BGPPeeringBuilder) resolveL2AVRF(l2a *nc.Layer2Attachment, data *resolver.ResolvedData) (string, *nc.VRFSpec) {
	if l2a.Spec.Destinations == nil {
		return "", nil
	}

	for _, resolved := range data.Destinations {
		if resolved.VRFSpec != nil && resolved.Spec.VRFRef != nil {
			// Return the backbone VRF name (spec.vrf), not the CRD name,
			// to match the L2A builder's fabricVRF map key convention.
			return resolved.VRFSpec.VRF, resolved.VRFSpec
		}
	}

	return "", nil
}

// buildListenRangePeers creates BGPPeer entries with ListenRange from Network CIDRs.
// Import filter is scoped to the Inbound addresses (what the workload may advertise).
// Export filter is permit-all (workload sees all VRF routes).
func (b *BGPPeeringBuilder) buildListenRangePeers(bp *nc.BGPPeering, net *resolver.ResolvedNetwork, inboundIPv4, inboundIPv6 []string) []networkv1alpha1.BGPPeer {
	var peers []networkv1alpha1.BGPPeer

	if net.Spec.IPv4 != nil {
		peer := b.buildBasePeer(bp)
		cidr := net.Spec.IPv4.CIDR
		peer.ListenRange = &cidr
		peer.IPv4 = b.buildPeerAF(bp, inboundIPv4, true)
		peers = append(peers, peer)
	}

	if net.Spec.IPv6 != nil {
		peer := b.buildBasePeer(bp)
		cidr := net.Spec.IPv6.CIDR
		peer.ListenRange = &cidr
		peer.IPv6 = b.buildPeerAF(bp, inboundIPv6, false)
		peers = append(peers, peer)
	}

	return peers
}

// resolveInboundAddresses collects IPv4 and IPv6 addresses from the Inbound CRDs
// referenced by the BGPPeering's inboundRefs.
func (*BGPPeeringBuilder) resolveInboundAddresses(bp *nc.BGPPeering, data *resolver.ResolvedData) (ipv4, ipv6 []string) {
	if bp.Spec.Ref.InboundRefs == nil {
		return nil, nil
	}

	refSet := make(map[string]struct{}, len(bp.Spec.Ref.InboundRefs))
	for _, ref := range bp.Spec.Ref.InboundRefs {
		refSet[ref] = struct{}{}
	}

	for i := range data.Inbounds {
		ib := &data.Inbounds[i]
		if _, ok := refSet[ib.Name]; !ok {
			continue
		}
		addrs := ib.Spec.Addresses
		if addrs == nil {
			addrs = ib.Status.Addresses
		}
		if addrs == nil {
			continue
		}
		ipv4 = append(ipv4, addrs.IPv4...)
		ipv6 = append(ipv6, addrs.IPv6...)
	}

	return ipv4, ipv6
}

// buildPeerAF builds an AddressFamily with an import filter from Inbound addresses
// and a permit-all export filter. isIPv4 controls the le value (32 vs 128).
func (*BGPPeeringBuilder) buildPeerAF(bp *nc.BGPPeering, prefixes []string, isIPv4 bool) *networkv1alpha1.AddressFamily {
	le := 128
	if isIPv4 {
		le = 32
	}

	importFilter := &networkv1alpha1.Filter{
		DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
	}
	for _, pfx := range prefixes {
		leCopy := le
		importFilter.Items = append(importFilter.Items, networkv1alpha1.FilterItem{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: pfx, Le: &leCopy},
			},
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
		})
	}

	af := &networkv1alpha1.AddressFamily{
		ImportFilter: importFilter,
		ExportFilter: &networkv1alpha1.Filter{
			DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
		},
	}
	if bp.Spec.MaximumPrefixes != nil {
		maxPfx := uint32(*bp.Spec.MaximumPrefixes) //nolint:gosec // value validated by CRD schema
		af.MaxPrefixes = &maxPfx
	}
	return af
}

// inboundEVPNExportItems builds EVPN export filter items from Inbound addresses
// so those prefixes are distributed across the fabric.
func (*BGPPeeringBuilder) inboundEVPNExportItems(ipv4, ipv6 []string) []networkv1alpha1.FilterItem {
	items := make([]networkv1alpha1.FilterItem, 0, len(ipv4)+len(ipv6))
	for _, pfx := range ipv4 {
		le := 32
		items = append(items, networkv1alpha1.FilterItem{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: pfx, Le: &le},
			},
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
		})
	}
	for _, pfx := range ipv6 {
		le := 128
		items = append(items, networkv1alpha1.FilterItem{
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: pfx, Le: &le},
			},
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
		})
	}
	return items
}

// buildBasePeer creates a BGPPeer with common fields from the BGPPeering spec.
func (*BGPPeeringBuilder) buildBasePeer(bp *nc.BGPPeering) networkv1alpha1.BGPPeer {
	peer := networkv1alpha1.BGPPeer{}

	if bp.Spec.WorkloadAS != nil {
		peer.RemoteASN = uint32(*bp.Spec.WorkloadAS) //nolint:gosec // value validated by CRD schema (positive integer)
	}

	peer.HoldTime = bp.Spec.HoldTime
	peer.KeepaliveTime = bp.Spec.KeepaliveTime

	if bp.Spec.EnableBFD != nil && *bp.Spec.EnableBFD && bp.Spec.BFDProfile != nil {
		peer.BFDProfile = &networkv1alpha1.BFDProfile{
			MinInterval: bp.Spec.BFDProfile.MinInterval,
		}
	}

	return peer
}

// buildAddressFamilies constructs IPv4/IPv6 address family config from the BGPPeering spec.
// For loopbackPeer mode, uses permit-all export and reject-default import (no inboundRefs).
func (*BGPPeeringBuilder) buildAddressFamilies(bp *nc.BGPPeering) (ipv4af, ipv6af *networkv1alpha1.AddressFamily) {
	permitAllExport := &networkv1alpha1.Filter{
		DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
	}
	rejectImport := &networkv1alpha1.Filter{
		DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
	}

	makeAF := func() *networkv1alpha1.AddressFamily {
		af := &networkv1alpha1.AddressFamily{
			ImportFilter: rejectImport,
			ExportFilter: permitAllExport,
		}
		if bp.Spec.MaximumPrefixes != nil {
			maxPfx := uint32(*bp.Spec.MaximumPrefixes) //nolint:gosec // value validated by CRD schema
			af.MaxPrefixes = &maxPfx
		}
		return af
	}

	if len(bp.Spec.AddressFamilies) == 0 {
		return makeAF(), makeAF()
	}

	for _, af := range bp.Spec.AddressFamilies {
		switch af {
		case nc.BGPAddressFamilyIPv4Unicast:
			ipv4af = makeAF()
		case nc.BGPAddressFamilyIPv6Unicast:
			ipv6af = makeAF()
		}
	}

	return ipv4af, ipv6af
}
