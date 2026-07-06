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
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
func (b *BGPPeeringBuilder) Build(ctx context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	logger := log.FromContext(ctx).WithName("bgppeering-builder")
	result := make(map[string]*NodeContribution)

	for i := range data.BGPPeerings {
		bp := &data.BGPPeerings[i]

		switch bp.Spec.Mode {
		case nc.BGPPeeringModeListenRange:
			if err := b.buildListenRange(bp, data, result); err != nil {
				logger.Info("skipping BGPPeering with unresolvable listenRange",
					"bgppeering", bp.Name, "error", err.Error())
				reportSkip(ctx, "BGPPeering", bp.Name, "ListenRangeUnresolved", err.Error())
				continue
			}
		case nc.BGPPeeringModeLoopbackPeer:
			b.buildLoopbackPeer(bp, data, result)
		default:
			logger.Info("skipping BGPPeering with unknown mode",
				"bgppeering", bp.Name, "mode", bp.Spec.Mode)
			reportSkip(ctx, "BGPPeering", bp.Name, "UnknownMode",
				fmt.Sprintf("unknown BGPPeering mode %q", bp.Spec.Mode))
			continue
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

	// Resolve the L2A's destination VRFs for the IRB. The Destinations
	// LabelSelector may match multiple Destinations across multiple VRFs; the
	// listen-range peer + EVPN export must be installed on every matched VRF.
	vrfs := b.resolveL2AVRFs(l2a, data)
	if len(vrfs) == 0 {
		return fmt.Errorf("Layer2Attachment %q has no VRF for IRB", l2a.Name)
	}

	// Resolve networkRefs to get the CIDRs L2 clients may announce. These
	// form the import allow-list and the EVPN export set. The listen-range
	// CIDR itself comes from the L2A's Network (net), not from here.
	allowIPv4, allowIPv6 := b.resolveNetworkCIDRs(bp, data)

	// Build BGPPeer with ListenRange, import filter from networkRefs CIDRs,
	// and EVPN export items for those same CIDRs.
	peers := b.buildListenRangePeers(bp, net, allowIPv4, allowIPv6, data)
	evpnExportItems := b.evpnExportItems(allowIPv4, allowIPv6)

	// Sorted iteration for deterministic output.
	vrfNames := make([]string, 0, len(vrfs))
	for n := range vrfs {
		vrfNames = append(vrfNames, n)
	}
	sort.Strings(vrfNames)

	// Apply to all nodes (no nodeSelector on BGPPeering).
	for i := range data.Nodes {
		node := &data.Nodes[i]
		contrib, ok := result[node.Name]
		if !ok {
			contrib = NewNodeContribution()
			result[node.Name] = contrib
		}

		for _, vrfName := range vrfNames {
			fvrf, exists := contrib.FabricVRFs[vrfName]
			if !exists {
				fvrf = buildFabricVRF(vrfs[vrfName])
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
	}

	return nil
}

// buildLoopbackPeer creates BGPPeer entries with Address on the ClusterVRF.
func (b *BGPPeeringBuilder) buildLoopbackPeer(bp *nc.BGPPeering, data *resolver.ResolvedData, result map[string]*NodeContribution) {
	peer := b.buildBasePeer(bp, data)
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

// resolveL2AVRFs returns ALL VRFs (name → spec) matched by the
// Layer2Attachment's Destinations selector. Returning a multi-VRF map lets the
// caller fan out the listen-range peer and EVPN export items across every
// matched VRF.
func (*BGPPeeringBuilder) resolveL2AVRFs(l2a *nc.Layer2Attachment, data *resolver.ResolvedData) map[string]*nc.VRFSpec {
	if l2a.Spec.Destinations == nil {
		return nil
	}
	return resolveSelectorVRFs(l2a.Spec.Destinations, data)
}

// buildListenRangePeers creates BGPPeer entries with ListenRange from the L2A
// Network CIDR. The import filter is scoped to the allow-list prefixes
// (networkRefs CIDRs — what the L2 clients may announce, matched le 32/128).
// Export filter is permit-all (workload sees all VRF routes).
func (b *BGPPeeringBuilder) buildListenRangePeers(bp *nc.BGPPeering, net *resolver.ResolvedNetwork, allowIPv4, allowIPv6 []string, data *resolver.ResolvedData) []networkv1alpha1.BGPPeer {
	var peers []networkv1alpha1.BGPPeer

	if net.Spec.IPv4 != nil {
		peer := b.buildBasePeer(bp, data)
		cidr := net.Spec.IPv4.CIDR
		peer.ListenRange = &cidr
		peer.IPv4 = b.buildPeerAF(bp, allowIPv4, true)
		peers = append(peers, peer)
	}

	if net.Spec.IPv6 != nil {
		peer := b.buildBasePeer(bp, data)
		cidr := net.Spec.IPv6.CIDR
		peer.ListenRange = &cidr
		peer.IPv6 = b.buildPeerAF(bp, allowIPv6, false)
		peers = append(peers, peer)
	}

	return peers
}

// resolveNetworkCIDRs collects the IPv4 and IPv6 CIDRs from the Network CRDs
// referenced by the BGPPeering's networkRefs. These CIDRs form the listenRange
// import allow-list (L2 clients may announce prefixes within them) and the
// EVPN export set.
func (*BGPPeeringBuilder) resolveNetworkCIDRs(bp *nc.BGPPeering, data *resolver.ResolvedData) (ipv4, ipv6 []string) {
	for _, ref := range bp.Spec.Ref.NetworkRefs {
		net, ok := data.Networks[ref]
		if !ok {
			continue
		}
		if net.Spec.IPv4 != nil && net.Spec.IPv4.CIDR != "" {
			ipv4 = append(ipv4, net.Spec.IPv4.CIDR)
		}
		if net.Spec.IPv6 != nil && net.Spec.IPv6.CIDR != "" {
			ipv6 = append(ipv6, net.Spec.IPv6.CIDR)
		}
	}
	return ipv4, ipv6
}

// buildPeerAF builds an AddressFamily with an import filter from the allow-list
// prefixes (networkRefs CIDRs) and a permit-all export filter. isIPv4 controls
// the le value (32 vs 128), so a CIDR accepts any more-specific prefix within it.
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

// evpnExportItems builds EVPN export filter items from the allow-list prefixes
// (networkRefs CIDRs) so those prefixes are distributed across the fabric.
func (*BGPPeeringBuilder) evpnExportItems(ipv4, ipv6 []string) []networkv1alpha1.FilterItem {
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
// When data is non-nil and a BGPPassword for bp is present, the password is
// inlined into the peer (resolved earlier from bp.Spec.AuthSecretRef).
func (*BGPPeeringBuilder) buildBasePeer(bp *nc.BGPPeering, data *resolver.ResolvedData) networkv1alpha1.BGPPeer {
	peer := networkv1alpha1.BGPPeer{}

	if bp.Spec.WorkloadAS != nil {
		peer.RemoteASN = uint32(*bp.Spec.WorkloadAS) //nolint:gosec // value validated by CRD schema: required, range [1, 4294967295]
	}

	peer.HoldTime = bp.Spec.HoldTime
	peer.KeepaliveTime = bp.Spec.KeepaliveTime

	if bp.Spec.EnableBFD != nil && *bp.Spec.EnableBFD && bp.Spec.BFDProfile != nil {
		peer.BFDProfile = &networkv1alpha1.BFDProfile{
			MinInterval: bp.Spec.BFDProfile.MinInterval,
		}
	}

	if data != nil && bp.Spec.AuthSecretRef != nil {
		key := client.ObjectKeyFromObject(bp).String()
		if pw, ok := data.BGPPasswords[key]; ok && pw != "" {
			peer.Password = &pw
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
