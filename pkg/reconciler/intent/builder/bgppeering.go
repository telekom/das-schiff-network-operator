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
func (b *BGPPeeringBuilder) Name() string {
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
			if err := b.buildLoopbackPeer(bp, data, result); err != nil {
				return nil, fmt.Errorf("BGPPeering %q (loopbackPeer) failed: %w", bp.Name, err)
			}
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

	// Build BGPPeer with ListenRange from Network CIDRs.
	peers := b.buildListenRangePeers(bp, net)

	// Apply to all nodes (no nodeSelector on BGPPeering).
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

		fvrf.BGPPeers = append(fvrf.BGPPeers, peers...)
		contrib.FabricVRFs[vrfName] = fvrf
	}

	return nil
}

// buildLoopbackPeer creates BGPPeer entries with Address on the ClusterVRF.
func (b *BGPPeeringBuilder) buildLoopbackPeer(bp *nc.BGPPeering, data *resolver.ResolvedData, result map[string]*NodeContribution) error { //nolint:unparam // error return kept for interface consistency
	peer := b.buildBasePeer(bp)
	// Loopback peer address is TBD — set to nil (auto-generated ULA by agent).

	// Build address families.
	peer.IPv4, peer.IPv6 = b.buildAddressFamilies(bp)

	// Apply to all nodes.
	for _, node := range data.Nodes {
		contrib, ok := result[node.Name]
		if !ok {
			contrib = NewNodeContribution()
			result[node.Name] = contrib
		}

		if contrib.ClusterVRF == nil {
			contrib.ClusterVRF = &networkv1alpha1.VRF{}
		}

		contrib.ClusterVRF.BGPPeers = append(contrib.ClusterVRF.BGPPeers, peer)
	}

	return nil
}

// resolveL2AVRF looks up a Layer2Attachment's destination VRF.
func (b *BGPPeeringBuilder) resolveL2AVRF(l2a *nc.Layer2Attachment, data *resolver.ResolvedData) (string, *nc.VRFSpec) {
	if l2a.Spec.Destinations == nil {
		return "", nil
	}

	for _, resolved := range data.Destinations {
		if resolved.VRFSpec != nil && resolved.Spec.VRFRef != nil {
			return *resolved.Spec.VRFRef, resolved.VRFSpec
		}
	}

	return "", nil
}

// buildListenRangePeers creates BGPPeer entries with ListenRange from Network CIDRs.
func (b *BGPPeeringBuilder) buildListenRangePeers(bp *nc.BGPPeering, net *resolver.ResolvedNetwork) []networkv1alpha1.BGPPeer {
	var peers []networkv1alpha1.BGPPeer

	if net.Spec.IPv4 != nil {
		peer := b.buildBasePeer(bp)
		cidr := net.Spec.IPv4.CIDR
		peer.ListenRange = &cidr
		peer.IPv4 = &networkv1alpha1.AddressFamily{}
		peers = append(peers, peer)
	}

	if net.Spec.IPv6 != nil {
		peer := b.buildBasePeer(bp)
		cidr := net.Spec.IPv6.CIDR
		peer.ListenRange = &cidr
		peer.IPv6 = &networkv1alpha1.AddressFamily{}
		peers = append(peers, peer)
	}

	return peers
}

// buildBasePeer creates a BGPPeer with common fields from the BGPPeering spec.
func (b *BGPPeeringBuilder) buildBasePeer(bp *nc.BGPPeering) networkv1alpha1.BGPPeer {
	peer := networkv1alpha1.BGPPeer{}

	if bp.Spec.WorkloadAS != nil {
		peer.RemoteASN = uint32(*bp.Spec.WorkloadAS)
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
func (b *BGPPeeringBuilder) buildAddressFamilies(bp *nc.BGPPeering) (*networkv1alpha1.AddressFamily, *networkv1alpha1.AddressFamily) {
	var ipv4, ipv6 *networkv1alpha1.AddressFamily

	if len(bp.Spec.AddressFamilies) == 0 {
		// Default: dual-stack.
		ipv4 = &networkv1alpha1.AddressFamily{}
		ipv6 = &networkv1alpha1.AddressFamily{}
		return ipv4, ipv6
	}

	for _, af := range bp.Spec.AddressFamilies {
		switch af {
		case nc.BGPAddressFamilyIPv4Unicast:
			ipv4 = &networkv1alpha1.AddressFamily{}
		case nc.BGPAddressFamilyIPv6Unicast:
			ipv6 = &networkv1alpha1.AddressFamily{}
		}
	}

	if bp.Spec.MaximumPrefixes != nil {
		maxPfx := uint32(*bp.Spec.MaximumPrefixes)
		if ipv4 != nil {
			ipv4.MaxPrefixes = &maxPfx
		}
		if ipv6 != nil {
			ipv6.MaxPrefixes = &maxPfx
		}
	}

	return ipv4, ipv6
}
