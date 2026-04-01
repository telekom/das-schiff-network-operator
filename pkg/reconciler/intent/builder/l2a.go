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
	"strings"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// L2ABuilder transforms Layer2Attachment intent CRDs into NNC Layer2 configs.
type L2ABuilder struct{}

// NewL2ABuilder creates a new L2ABuilder.
func NewL2ABuilder() *L2ABuilder {
	return &L2ABuilder{}
}

// Name returns the builder name.
func (b *L2ABuilder) Name() string {
	return "l2a"
}

// Build produces per-node Layer2 configurations from Layer2Attachment resources.
func (b *L2ABuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	result := make(map[string]*NodeContribution)

	for i := range data.Layer2Attachments {
		l2a := &data.Layer2Attachments[i]

		// Resolve the referenced Network.
		net, ok := data.Networks[l2a.Spec.NetworkRef]
		if !ok {
			return nil, fmt.Errorf("Layer2Attachment %q references unknown Network %q", l2a.Name, l2a.Spec.NetworkRef)
		}

		// Resolve destinations to find VRF for IRB.
		vrfName, vrfSpec, err := b.resolveDestinationVRF(l2a, data)
		if err != nil {
			return nil, fmt.Errorf("Layer2Attachment %q destination resolution failed: %w", l2a.Name, err)
		}

		// Build the Layer2 config from Network + L2A fields.
		layer2, err := b.buildLayer2(l2a, net, vrfName, vrfSpec)
		if err != nil {
			return nil, fmt.Errorf("Layer2Attachment %q config build failed: %w", l2a.Name, err)
		}

		// Compute the map key (VLAN ID as string, matching legacy format).
		mapKey := fmt.Sprintf("%d", b.vlanID(net))

		// Determine which nodes this L2A applies to.
		matchingNodes, err := matchNodes(data.Nodes, l2a.Spec.NodeSelector)
		if err != nil {
			return nil, fmt.Errorf("Layer2Attachment %q node selector error: %w", l2a.Name, err)
		}

		for _, node := range matchingNodes {
			contrib, ok := result[node.Name]
			if !ok {
				contrib = NewNodeContribution()
				result[node.Name] = contrib
			}
			contrib.Layer2s[mapKey] = *layer2

			// If VRF is resolved, ensure the FabricVRF entry exists and
			// add network subnets to EVPN export filter + cluster VRFImport.
			if vrfName != "" && vrfSpec != nil {
				fvrf, exists := contrib.FabricVRFs[vrfName]
				if !exists {
					fvrf = buildFabricVRF(vrfSpec)
				}
				fvrf = addNetworkToFabricVRF(fvrf, net)
				contrib.FabricVRFs[vrfName] = fvrf
			}
		}
	}

	return result, nil
}

// resolveDestinationVRF finds the VRF for IRB plumbing by selecting Destinations
// matching the L2A's destination selector.
func (b *L2ABuilder) resolveDestinationVRF(l2a *nc.Layer2Attachment, data *resolver.ResolvedData) (string, *nc.VRFSpec, error) {
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
			if ok && resolved.VRFSpec != nil && resolved.Spec.VRFRef != nil {
				// Return the backbone VRF name (spec.vrf), not the CRD name.
				// The CRA agent uses this as the FabricVRF map key to create
				// VRF links named "s-<key>", so it must match legacy format.
				return resolved.VRFSpec.VRF, resolved.VRFSpec, nil
			}
		}
	}

	return "", nil, nil // no matching destination with VRF
}

// buildLayer2 creates a NNC Layer2 from a Layer2Attachment and its resolved Network.
func (b *L2ABuilder) buildLayer2(l2a *nc.Layer2Attachment, net *resolver.ResolvedNetwork, vrfName string, vrfSpec *nc.VRFSpec) (*networkv1alpha1.Layer2, error) {
	layer2 := &networkv1alpha1.Layer2{
		VNI:         uint32(b.vniValue(net)),
		VLAN:        uint16(b.vlanID(net)),
		RouteTarget: b.routeTarget(net, vrfSpec),
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
func (b *L2ABuilder) buildIRB(l2a *nc.Layer2Attachment, net *resolver.ResolvedNetwork, vrfName string) (*networkv1alpha1.IRB, error) {
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
func (b *L2ABuilder) vlanID(net *resolver.ResolvedNetwork) int32 {
	if net.Spec.VLAN != nil {
		return *net.Spec.VLAN
	}
	return 0
}

// vniValue extracts the VNI from a Network, defaulting to 0 if unset.
func (b *L2ABuilder) vniValue(net *resolver.ResolvedNetwork) int32 {
	if net.Spec.VNI != nil {
		return *net.Spec.VNI
	}
	return 0
}

// mtu extracts the MTU from a Layer2Attachment, defaulting to 1500.
func (b *L2ABuilder) mtu(l2a *nc.Layer2Attachment) uint16 {
	if l2a.Spec.MTU != nil {
		return uint16(*l2a.Spec.MTU)
	}
	return 1500
}

// routeTarget derives a route target. If the VRF has one, use it; otherwise empty.
func (b *L2ABuilder) routeTarget(net *resolver.ResolvedNetwork, vrfSpec *nc.VRFSpec) string {
	if vrfSpec != nil && vrfSpec.RouteTarget != nil {
		return *vrfSpec.RouteTarget
	}
	return ""
}

// buildFabricVRF creates a FabricVRF entry for a resolved VRF.
// Includes the cluster VRFImport required for fabric-to-cluster routing.
func buildFabricVRF(vrfSpec *nc.VRFSpec) networkv1alpha1.FabricVRF {
	fvrf := networkv1alpha1.FabricVRF{
		EVPNExportFilter: &networkv1alpha1.Filter{
			DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		},
		VRF: networkv1alpha1.VRF{
			VRFImports: []networkv1alpha1.VRFImport{
				{
					FromVRF: "cluster",
					Filter: networkv1alpha1.Filter{
						DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
					},
				},
			},
		},
	}

	if vrfSpec.VNI != nil {
		fvrf.VNI = uint32(*vrfSpec.VNI)
	}

	if vrfSpec.RouteTarget != nil {
		fvrf.EVPNImportRouteTargets = []string{*vrfSpec.RouteTarget}
		fvrf.EVPNExportRouteTargets = []string{*vrfSpec.RouteTarget}
	}

	return fvrf
}

// addNetworkToFabricVRF adds a Network's CIDRs to the FabricVRF's EVPN export filter
// and cluster VRFImport filter. This ensures subnets are exported via EVPN and
// imported from the cluster VRF into the fabric VRF.
func addNetworkToFabricVRF(fvrf networkv1alpha1.FabricVRF, net *resolver.ResolvedNetwork) networkv1alpha1.FabricVRF {
	items := networkCIDRFilterItems(net)
	if len(items) == 0 {
		return fvrf
	}

	// Add to EVPN export filter.
	if fvrf.EVPNExportFilter == nil {
		fvrf.EVPNExportFilter = &networkv1alpha1.Filter{
			DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		}
	}
	fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, items...)

	// Add to cluster VRFImport filter (first import is always "cluster").
	if len(fvrf.VRFImports) > 0 {
		fvrf.VRFImports[0].Filter.Items = append(fvrf.VRFImports[0].Filter.Items, items...)
	}

	return fvrf
}

// networkCIDRFilterItems creates FilterItems for a Network's IPv4 and IPv6 CIDRs.
func networkCIDRFilterItems(net *resolver.ResolvedNetwork) []networkv1alpha1.FilterItem {
	var items []networkv1alpha1.FilterItem
	if net.Spec.IPv4 != nil && net.Spec.IPv4.CIDR != "" {
		le := 32
		items = append(items, networkv1alpha1.FilterItem{
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{
					Prefix: net.Spec.IPv4.CIDR,
					Le:     &le,
				},
			},
		})
	}
	if net.Spec.IPv6 != nil && net.Spec.IPv6.CIDR != "" {
		le := 128
		items = append(items, networkv1alpha1.FilterItem{
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{
					Prefix: net.Spec.IPv6.CIDR,
					Le:     &le,
				},
			},
		})
	}
	return items
}

// addressFilterItems creates FilterItems for a list of CIDR addresses.
func addressFilterItems(addresses []string) []networkv1alpha1.FilterItem {
	var items []networkv1alpha1.FilterItem
	for _, addr := range addresses {
		le := 32
		if strings.Contains(addr, ":") {
			le = 128
		}
		items = append(items, networkv1alpha1.FilterItem{
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{
					Prefix: addr,
					Le:     &le,
				},
			},
		})
	}
	return items
}

// matchNodes returns nodes matching a label selector. If selector is nil, all nodes match.
func matchNodes(nodes []corev1.Node, selector *metav1.LabelSelector) ([]corev1.Node, error) {
	if selector == nil {
		return nodes, nil
	}

	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	var matched []corev1.Node
	for i := range nodes {
		if sel.Matches(labels.Set(nodes[i].Labels)) {
			matched = append(matched, nodes[i])
		}
	}

	return matched, nil
}
