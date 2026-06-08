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
	gonet "net"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

const defaultMTU = 1500

// L2ABuilder transforms Layer2Attachment intent CRDs into NNC Layer2 configs.
type L2ABuilder struct{}

// NewL2ABuilder creates a new L2ABuilder.
func NewL2ABuilder() *L2ABuilder {
	return &L2ABuilder{}
}

// Name returns the builder name.
func (*L2ABuilder) Name() string {
	return "l2a"
}

// Build produces per-node Layer2 configurations from Layer2Attachment resources.
func (b *L2ABuilder) Build(ctx context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	logger := log.FromContext(ctx).WithName("l2a-builder")
	result := make(map[string]*NodeContribution)
	// Track which L2A owns each interface name per node.
	// Key: "node/ifName", value: L2A name.
	ifOwner := make(map[string]string)

	for i := range data.Layer2Attachments {
		l2a := &data.Layer2Attachments[i]

		// Resolve the referenced Network — skip L2As with dangling refs.
		net, ok := data.Networks[l2a.Spec.NetworkRef]
		if !ok {
			logger.Info("skipping Layer2Attachment with unknown Network reference",
				"l2a", l2a.Name, "networkRef", l2a.Spec.NetworkRef)
			continue
		}

		// Resolve destinations to find VRF for IRB — skip on resolution errors.
		vrfName, vrfSpec, err := b.resolveDestinationVRF(l2a, data)
		if err != nil {
			logger.Info("skipping Layer2Attachment with unresolvable destinations",
				"l2a", l2a.Name, "error", err.Error())
			continue
		}

		if err := b.applyL2AToNodes(l2a, net, vrfName, vrfSpec, data, result, ifOwner); err != nil {
			logger.Info("skipping Layer2Attachment", "l2a", l2a.Name, "error", err.Error())
			reportSkip(ctx, "Layer2Attachment", l2a.Name, "BuildFailed", err.Error())
			continue
		}
	}

	return result, nil
}

// applyL2AToNodes fans out a single L2A to every matching node.
//
// It runs in two phases: a validation phase that resolves everything which can
// fail (node selector, Layer2 config / route-target collision, the IRB
// AnnouncementPolicy, and interface-name ownership) without mutating any state,
// followed by a mutation phase that cannot fail. This guarantees a misconfigured
// L2A is skipped cleanly by the caller without leaving partially-applied node
// contributions or interface claims behind.
func (b *L2ABuilder) applyL2AToNodes(
	l2a *nc.Layer2Attachment,
	net *resolver.ResolvedNetwork,
	vrfName string,
	vrfSpec *nc.VRFSpec,
	data *resolver.ResolvedData,
	result map[string]*NodeContribution,
	ifOwner map[string]string,
) error {
	mapKey := fmt.Sprintf("%d", b.vlanID(net))

	matchingNodes, err := matchNodes(data.Nodes, l2a.Spec.NodeSelector)
	if err != nil {
		return fmt.Errorf("Layer2Attachment %q node selector error: %w", l2a.Name, err)
	}

	// Layer2 config is node-independent; this guards the L2/L3 route-target collision.
	layer2, err := b.buildLayer2(l2a, net, vrfName, vrfSpec)
	if err != nil {
		return fmt.Errorf("Layer2Attachment %q config build failed: %w", l2a.Name, err)
	}

	// Resolve the IRB AnnouncementPolicy once (node-independent).
	var ap *nc.AnnouncementPolicy
	if vrfName != "" && vrfSpec != nil {
		ap, err = findMatchingAP(l2a.Labels, vrfName, data)
		if err != nil {
			return fmt.Errorf("Layer2Attachment %q: %w", l2a.Name, err)
		}
	}

	// Detect interface-name ownership conflicts across L2As before claiming any.
	var ifKeys []string
	if l2a.Spec.InterfaceName != nil && *l2a.Spec.InterfaceName != "" {
		for i := range matchingNodes {
			ifKey := matchingNodes[i].Name + "/" + *l2a.Spec.InterfaceName
			if prev, exists := ifOwner[ifKey]; exists {
				return fmt.Errorf("Layer2Attachments %q and %q both claim interface name %q on node %q",
					prev, l2a.Name, *l2a.Spec.InterfaceName, matchingNodes[i].Name)
			}
			ifKeys = append(ifKeys, ifKey)
		}
	}

	// Mutation phase — validation passed, so nothing below can fail.
	for _, ifKey := range ifKeys {
		ifOwner[ifKey] = l2a.Name
	}

	for i := range matchingNodes {
		node := &matchingNodes[i]
		contrib := ensureContrib(result, node.Name)
		contrib.Layer2s[mapKey] = *layer2

		// Carry netplan-only device info for this VLAN (interface name/parent
		// overrides plus, when enabled, per-node IPs). Kept off the NNC API.
		if dev, ok := buildNetplanDevice(l2a, net, node.Name); ok {
			contrib.NetplanNodeIPs[mapKey] = dev
		}

		if vrfName != "" && vrfSpec != nil {
			b.applyVRFContrib(net, vrfName, vrfSpec, contrib, ap)
		}
	}

	return nil
}

// applyVRFContrib updates the FabricVRF entry for a single node from an L2A.
func (*L2ABuilder) applyVRFContrib(
	net *resolver.ResolvedNetwork,
	vrfName string,
	vrfSpec *nc.VRFSpec,
	contrib *NodeContribution,
	ap *nc.AnnouncementPolicy,
) {
	fvrf, exists := contrib.FabricVRFs[vrfName]
	if !exists {
		fvrf = buildFabricVRF(vrfSpec)
	}
	fvrf = addNetworkToFabricVRF(&fvrf, net, ap)
	addAggregateRoutes(&fvrf, net, ap)
	contrib.FabricVRFs[vrfName] = fvrf
}

// resolveDestinationVRF finds the VRF for IRB plumbing by selecting Destinations
// matching the L2A's destination selector.
func (*L2ABuilder) resolveDestinationVRF(l2a *nc.Layer2Attachment, data *resolver.ResolvedData) (string, *nc.VRFSpec, error) {
	if l2a.Spec.Destinations == nil {
		return "", nil, nil // no VRF plumbing requested
	}

	selector, err := metav1.LabelSelectorAsSelector(l2a.Spec.Destinations)
	if err != nil {
		return "", nil, fmt.Errorf("invalid destination selector: %w", err)
	}

	// Match against raw Destination CRDs using their labels.
	for i := range data.RawDestinations {
		rawDest := &data.RawDestinations[i]
		if selector.Matches(labels.Set(rawDest.Labels)) {
			resolved, ok := data.Destinations[rawDest.Name]
			if ok && resolved.VRFSpec != nil {
				return resolved.VRFSpec.VRF, resolved.VRFSpec, nil
			}
		}
	}

	return "", nil, nil // no matching destination with VRF
}

// buildLayer2 creates a NNC Layer2 from a Layer2Attachment and its resolved Network.
func (b *L2ABuilder) buildLayer2(l2a *nc.Layer2Attachment, net *resolver.ResolvedNetwork, vrfName string, vrfSpec *nc.VRFSpec) (*networkv1alpha1.Layer2, error) {
	rt := b.routeTarget(net, vrfSpec)

	// Guard: L2 VNI must never share a route target with the L3 VRF.
	// A shared RT causes FRR to import link-local type-2 routes (which lack
	// RMAC) into the VRF, corrupting nexthop router MACs for EVPN type-5.
	if rt != "" && vrfSpec != nil && vrfSpec.RouteTarget != nil && rt == *vrfSpec.RouteTarget {
		return nil, fmt.Errorf("Layer2Attachment %q: L2 VNI route target %q must not equal VRF %q route target — this causes EVPN RMAC corruption",
			l2a.Name, rt, vrfSpec.VRF)
	}

	layer2 := &networkv1alpha1.Layer2{
		VNI:         uint32(b.vniValue(net)), //nolint:gosec // value validated by CRD schema (positive integer)
		VLAN:        uint16(b.vlanID(net)),   //nolint:gosec // value validated by CRD schema (positive integer)
		RouteTarget: rt,
		MTU:         b.mtu(l2a),
	}

	// Build IRB if anycast is not disabled and we have a VRF.
	if vrfName != "" && (l2a.Spec.DisableAnycast == nil || !*l2a.Spec.DisableAnycast) {
		irb, err := b.buildIRB(l2a, net, vrfName)
		if err != nil {
			return nil, err
		}
		layer2.IRB = irb
	}

	return layer2, nil
}

// buildIRB constructs the IRB config for an L2A with VRF plumbing.
func (*L2ABuilder) buildIRB(_ *nc.Layer2Attachment, net *resolver.ResolvedNetwork, vrfName string) (*networkv1alpha1.IRB, error) {
	irb := &networkv1alpha1.IRB{
		VRF: vrfName,
	}

	// Collect anycast gateway IPs from the Network CIDR.
	// The gateway address is typically the first usable IP in the subnet.
	var ipAddresses []string
	if net.Spec.IPv4 != nil {
		ipAddresses = append(ipAddresses, net.Spec.IPv4.CIDR)
	}
	if net.Spec.IPv6 != nil {
		ipAddresses = append(ipAddresses, net.Spec.IPv6.CIDR)
	}

	if len(ipAddresses) == 0 {
		return nil, fmt.Errorf("network %q has no IP address pools for IRB", net.Name)
	}

	irb.IPAddresses = ipAddresses
	// Default anycast MAC — agents may override with a node-specific MAC.
	irb.MACAddress = "00:00:5e:00:01:01"

	return irb, nil
}

// vlanID extracts the VLAN ID from a Network, defaulting to 0 if unset.
func (*L2ABuilder) vlanID(net *resolver.ResolvedNetwork) int32 {
	if net.Spec.VLAN != nil {
		return *net.Spec.VLAN
	}
	return 0
}

// vniValue extracts the VNI from a Network, defaulting to 0 if unset.
func (*L2ABuilder) vniValue(net *resolver.ResolvedNetwork) int32 {
	if net.Spec.VNI != nil {
		return *net.Spec.VNI
	}
	return 0
}

func (*L2ABuilder) mtu(l2a *nc.Layer2Attachment) uint16 {
	if l2a.Spec.MTU != nil {
		return uint16(*l2a.Spec.MTU) //nolint:gosec // value validated by CRD schema (positive integer)
	}
	return defaultMTU
}

// routeTarget returns an empty string so that FRR auto-derives the L2 VNI's
// route target. The L3 VRF RT is injected automatically by FRR for non-link-local
// type-2 routes via build_evpn_route_extcomm — setting the L2 VNI RT to the
// VRF's RT would cause link-local type-2 routes (which lack RMAC) to be imported
// into the VRF, corrupting the nexthop router MAC.
func (*L2ABuilder) routeTarget(_ *resolver.ResolvedNetwork, _ *nc.VRFSpec) string {
	return ""
}

// buildNetplanDevice assembles the netplan-only device info for a VLAN on a
// node: optional interface name/parent overrides plus, when NodeIPConfig is
// enabled, the per-node IP addresses and IRB anycast gateways. It returns false
// when there is nothing to carry. This data is intentionally kept off the
// NodeNetworkConfig API and only rendered into the NodeNetplanConfig.
func buildNetplanDevice(l2a *nc.Layer2Attachment, nw *resolver.ResolvedNetwork, nodeName string) (NetplanNodeIP, bool) {
	var dev NetplanNodeIP
	if l2a.Spec.InterfaceName != nil && *l2a.Spec.InterfaceName != "" {
		dev.InterfaceName = *l2a.Spec.InterfaceName
	}
	if l2a.Spec.InterfaceRef != nil && *l2a.Spec.InterfaceRef != "" {
		dev.InterfaceRef = *l2a.Spec.InterfaceRef
	}
	if l2a.Spec.NodeIPs != nil && l2a.Spec.NodeIPs.Enabled {
		if nodeIP := buildNetplanNodeIP(l2a, nw, nodeName); nodeIP != nil {
			dev.Addresses = nodeIP.Addresses
			dev.Gateways = nodeIP.Gateways
		}
	}
	if dev.InterfaceName == "" && dev.InterfaceRef == "" && len(dev.Addresses) == 0 && len(dev.Gateways) == 0 {
		return NetplanNodeIP{}, false
	}
	return dev, true
}

// buildNetplanNodeIP creates a NetplanNodeIP for a node from the L2A's allocated
// per-node addresses and the Network's CIDRs. The addresses get the network's
// prefix length, and routes point to the IRB anycast gateway.
func buildNetplanNodeIP(l2a *nc.Layer2Attachment, nw *resolver.ResolvedNetwork, nodeName string) *NetplanNodeIP {
	alloc, ok := l2a.Status.NodeAddresses[nodeName]
	if !ok {
		return nil
	}

	result := &NetplanNodeIP{}

	if nw.Spec.IPv4 != nil && len(alloc.IPv4) > 0 {
		gwIP, prefixLen := parseCIDRParts(nw.Spec.IPv4.CIDR)
		if gwIP != "" {
			for _, ip := range alloc.IPv4 {
				result.Addresses = append(result.Addresses, fmt.Sprintf("%s/%s", ip, prefixLen))
			}
			result.Gateways = append(result.Gateways, gwIP)
		}
	}

	if nw.Spec.IPv6 != nil && len(alloc.IPv6) > 0 {
		gwIP, prefixLen := parseCIDRParts(nw.Spec.IPv6.CIDR)
		if gwIP != "" {
			for _, ip := range alloc.IPv6 {
				result.Addresses = append(result.Addresses, fmt.Sprintf("%s/%s", ip, prefixLen))
			}
			result.Gateways = append(result.Gateways, gwIP)
		}
	}

	if len(result.Addresses) == 0 {
		return nil
	}
	return result
}

// parseCIDRParts extracts the host IP and prefix length string from a CIDR
// like "10.0.1.1/24" → ("10.0.1.1", "24").
func parseCIDRParts(cidr string) (gatewayIP, prefixLen string) {
	ip, ipNet, err := gonet.ParseCIDR(cidr)
	if err != nil {
		return "", ""
	}
	ones, _ := ipNet.Mask.Size()

	// The Network CIDR host part is the gateway (e.g., "10.0.1.1/24" → gw "10.0.1.1").
	gwIP := ip.String()
	if strings.Contains(cidr, ":") && ip.To4() == nil {
		gwIP = ip.String()
	}
	return gwIP, fmt.Sprintf("%d", ones)
}
