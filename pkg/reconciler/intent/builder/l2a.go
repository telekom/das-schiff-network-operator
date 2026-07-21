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
	"errors"
	"fmt"
	stdnet "net"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/ipmath"
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
	// Track which L2A owns each per-node netplan slot and device name.
	// Keys are namespaced: "<node>\x00slot\x00<mapKey>" and
	// "<node>\x00dev\x00<deviceName>"; value is the owning L2A name.
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
			// Surface the misconfiguration (e.g. an invalid destinations
			// selector) as Ready=False, not just a log line.
			logger.Info("skipping Layer2Attachment with unresolvable destinations",
				"l2a", l2a.Name, "error", err.Error())
			reportSkip(ctx, "Layer2Attachment", l2a.Namespace, l2a.Name, skipReason(err), err.Error())
			continue
		}

		if err := b.applyL2AToNodes(l2a, net, vrfName, vrfSpec, data, result, ifOwner); err != nil {
			// Never abort the reconcile for one bad L2A: skip it and surface the
			// failure as a Ready=False condition (with a specific reason) so it
			// is visible in the resource status, not only the controller log.
			logger.Info("skipping Layer2Attachment", "l2a", l2a.Name, "error", err.Error())
			reportSkip(ctx, "Layer2Attachment", l2a.Namespace, l2a.Name, skipReason(err), err.Error())
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
	vlanID := b.vlanID(net)
	mapKey := netplanMapKey(vlanID, l2a)

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

	// Detect ownership conflicts across L2As before claiming any. Two kinds of
	// collision are surfaced (the offending L2A is skipped with Ready=False)
	// rather than silently overwriting depending on list order:
	//   - contribution slot (mapKey): the key into contrib.Layer2s /
	//     NetplanNodeIPs. Two L2As sharing a mapKey on a node — the same VLAN ID,
	//     or the same native interfaceRef — would overwrite each other.
	//   - rendered device name: the netplan interface the config lands on (the
	//     parent interfaceRef in native mode, or the InterfaceName override for a
	//     tagged VLAN). Two L2As on different VLANs but the same device name also
	//     collide.
	claims := ownershipClaims(matchingNodes, mapKey, netplanClaimName(net, l2a))
	for i := range claims {
		if prev, exists := ifOwner[claims[i].key]; exists {
			return fmt.Errorf("Layer2Attachments %q and %q both configure %s on node %q",
				prev, l2a.Name, claims[i].what, claims[i].node)
		}
	}

	// Destination-derived static routes are node-independent; compute once. An
	// invalid destinations selector is a configuration error that must surface
	// (Ready=False), so validate it here before the mutation phase.
	routes, err := destinationRoutes(l2a, data)
	if err != nil {
		return fmt.Errorf("Layer2Attachment %q: %w", l2a.Name, err)
	}

	// Mutation phase — validation passed, so nothing below can fail.
	for i := range claims {
		ifOwner[claims[i].key] = l2a.Name
	}

	for i := range matchingNodes {
		node := &matchingNodes[i]
		contrib := ensureContrib(result, node.Name)
		if layer2 != nil {
			contrib.Layer2s[mapKey] = *layer2
		}

		// Carry netplan-only device info for this VLAN (interface name/parent
		// overrides plus, when enabled, per-node IPs). Kept off the NNC API.
		if dev, ok := buildNetplanDevice(l2a, net, node.Name, routes); ok {
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
		return nil, fmt.Errorf("L2 VNI route target %q must not equal VRF %q route target — this causes EVPN RMAC corruption",
			rt, vrfSpec.VRF)
	}

	if net.Spec.VNI == nil {
		// Pure L2 mode: no NNC Layer2 entry. interfaceRef must be set so the
		// netplan agent knows which parent interface to configure.
		if l2a.Spec.InterfaceRef == nil || *l2a.Spec.InterfaceRef == "" {
			return nil, errors.New("network has no VNI (pure L2 mode) but interfaceRef is not set — cannot determine parent interface")
		}
		return nil, nil
	}

	// HBN mode requires a VLAN: without it the NNC Layer2.VLAN field would be
	// 0 which the API server rejects (minimum 1).
	if net.Spec.VLAN == nil {
		return nil, fmt.Errorf("network has VNI %d but no VLAN — HBN mode requires both VNI and VLAN", *net.Spec.VNI)
	}

	// interfaceRef is a non-HBN (pure L2) concept; an HBN Network (VNI set)
	// plumbs its VLAN onto the out-of-band trunk. Allowing interfaceRef here
	// would re-parent the HBN VLAN and contradicts the documented contract.
	if l2a.Spec.InterfaceRef != nil && *l2a.Spec.InterfaceRef != "" {
		return nil, errors.New("interfaceRef is set but Network has a VNI (HBN mode) — interfaceRef is only valid for non-HBN (pure L2) Networks without a VNI")
	}

	layer2 := &networkv1alpha1.Layer2{
		VNI:         uint32(b.vniValue(net)), //nolint:gosec // value validated by CRD schema (positive integer)
		VLAN:        uint16(b.vlanID(net)),   //nolint:gosec // value validated by CRD schema (positive integer)
		RouteTarget: rt,
		MTU:         b.mtu(l2a),
		// Stamp the originating Layer2Attachment identity so routed-CNI L2 port
		// attachments can bind to this L2 domain by reference (rather than VNI).
		AttachmentRef: &networkv1alpha1.Layer2AttachmentRef{
			Name:      l2a.Name,
			Namespace: l2a.Namespace,
		},
	}

	// Build IRB if anycast is not disabled and we have a VRF.
	if vrfName != "" && (l2a.Spec.DisableAnycast == nil || !*l2a.Spec.DisableAnycast) {
		irb, err := b.buildIRB(l2a, net, vrfName)
		if err != nil {
			// A Network CIDR with no usable gateway (e.g. a /32 or /128) is a
			// configuration error, not a reason to abort the reconcile. Tag it
			// with a specific reason so the L2A's Ready condition is actionable.
			return nil, &skipReasonError{reason: reasonInvalidIRBGateway, err: err}
		}
		layer2.IRB = irb
	}

	return layer2, nil
}

// reasonInvalidIRBGateway is the Ready-condition reason used when an L2A's
// referenced Network CIDR yields no usable anycast gateway (e.g. /32, /128).
const reasonInvalidIRBGateway = "InvalidIRBGateway"

// skipReasonError wraps a build error with a specific condition reason to
// report. Builders return it so a skipped resource surfaces an actionable
// Ready=False reason instead of the generic "BuildFailed".
type skipReasonError struct {
	reason string
	err    error
}

func (e *skipReasonError) Error() string { return e.err.Error() }
func (e *skipReasonError) Unwrap() error { return e.err }

// skipReason returns the condition reason to report for a build error,
// defaulting to "BuildFailed" when the error carries no specific reason.
func skipReason(err error) string {
	var s *skipReasonError
	if errors.As(err, &s) {
		return s.reason
	}
	return "BuildFailed"
}

// buildIRB constructs the IRB config for an L2A with VRF plumbing.
func (*L2ABuilder) buildIRB(_ *nc.Layer2Attachment, net *resolver.ResolvedNetwork, vrfName string) (*networkv1alpha1.IRB, error) {
	irb := &networkv1alpha1.IRB{
		VRF: vrfName,
	}

	// Collect anycast gateway IPs from the Network CIDR. The Network resource
	// carries the network address (host bits zero); the anycast gateway is the
	// first usable host (network address + 1), preserving the prefix length.
	// See ipmath.GatewayCIDR for point-to-point (/31, /127) and single-host handling.
	var ipAddresses []string
	if net.Spec.IPv4 != nil {
		gw, err := ipmath.GatewayCIDR(net.Spec.IPv4.CIDR)
		if err != nil {
			return nil, fmt.Errorf("network %q IPv4 CIDR: %w", net.Name, err)
		}
		ipAddresses = append(ipAddresses, gw)
	}
	if net.Spec.IPv6 != nil {
		gw, err := ipmath.GatewayCIDR(net.Spec.IPv6.CIDR)
		if err != nil {
			return nil, fmt.Errorf("network %q IPv6 CIDR: %w", net.Name, err)
		}
		ipAddresses = append(ipAddresses, gw)
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

// netplanMapKey produces a key for the per-node NetplanNodeIPs map.
// For tagged VLANs this is the VLAN ID, allowing VLAN-based dedup.
// For native VLAN (vlanID == 0) this includes the interface reference to
// prevent collisions when multiple pure-L2 attachments use different NICs.
func netplanMapKey(vlanID int32, l2a *nc.Layer2Attachment) string {
	if vlanID != 0 {
		return fmt.Sprintf("%d", vlanID)
	}
	if l2a.Spec.InterfaceRef != nil && *l2a.Spec.InterfaceRef != "" {
		return fmt.Sprintf("eth:%s", *l2a.Spec.InterfaceRef)
	}
	return "0"
}

// netplanClaimName returns the netplan device name whose config this L2A owns
// on a node. In native/untagged mode the config lands directly on the parent
// interfaceRef (InterfaceName is ignored). For a tagged VLAN it is the
// InterfaceName override when set, otherwise the default "vlan.<vlan>". Two L2As
// claiming the same name on the same node conflict. Returns "" only when the
// name cannot be determined (native mode without an interfaceRef).
func netplanClaimName(net *resolver.ResolvedNetwork, l2a *nc.Layer2Attachment) string {
	if net.Spec.VLAN == nil {
		if l2a.Spec.InterfaceRef != nil && *l2a.Spec.InterfaceRef != "" {
			return *l2a.Spec.InterfaceRef
		}
		return ""
	}
	if l2a.Spec.InterfaceName != nil && *l2a.Spec.InterfaceName != "" {
		return *l2a.Spec.InterfaceName
	}
	return fmt.Sprintf("vlan.%d", *net.Spec.VLAN)
}

// claim is a per-node ownership claim used to detect L2A conflicts.
type claim struct {
	key  string // namespaced ownership key stored in ifOwner
	what string // human-readable subject for the error message
	node string
}

// ownershipClaims builds the per-node ownership claims for an L2A: the
// contribution slot (mapKey, always) and the rendered netplan device name
// (devName, when it lands on a fixed interface). Keys are namespaced so a
// mapKey and a device name can never alias each other.
func ownershipClaims(nodes []corev1.Node, mapKey, devName string) []claim {
	// Up to two claims per node: the contribution slot and, optionally, a device name.
	const claimsPerNode = 2
	claims := make([]claim, 0, len(nodes)*claimsPerNode)
	for i := range nodes {
		n := nodes[i].Name
		claims = append(claims, claim{
			key:  n + "\x00slot\x00" + mapKey,
			what: "netplan slot " + mapKey,
			node: n,
		})
		if devName != "" {
			claims = append(claims, claim{
				key:  n + "\x00dev\x00" + devName,
				what: "interface " + devName,
				node: n,
			})
		}
	}
	return claims
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
func buildNetplanDevice(l2a *nc.Layer2Attachment, nw *resolver.ResolvedNetwork, nodeName string, routes []NetplanRoute) (NetplanNodeIP, bool) {
	var dev NetplanNodeIP
	tagged := nw.Spec.VLAN != nil
	if tagged {
		dev.VLAN = uint16(*nw.Spec.VLAN) //nolint:gosec
	}
	// Default the MTU only for tagged VLAN sub-interfaces (which the operator
	// creates). A native/untagged device targets a pre-existing parent NIC, so
	// only carry an MTU when the user explicitly overrides it — otherwise we
	// would force the parent MTU (e.g. shrink a jumbo link to 1500).
	if l2a.Spec.MTU != nil {
		dev.MTU = uint16(*l2a.Spec.MTU) //nolint:gosec
	} else if tagged {
		dev.MTU = defaultMTU
	}
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
	dev.Routes = routes

	if netplanDeviceEmpty(&dev, tagged, l2a.Spec.MTU != nil) {
		return NetplanNodeIP{}, false
	}
	return dev, true
}

// netplanDeviceEmpty reports whether a netplan device carries nothing
// actionable. A native (untagged) device with only an interfaceRef and no
// addresses/routes/MTU override is skipped so the operator does not mutate the
// parent link merely to declare it.
func netplanDeviceEmpty(dev *NetplanNodeIP, tagged, hasMTUOverride bool) bool {
	hasPayload := len(dev.Addresses) > 0 || len(dev.Gateways) > 0 || len(dev.Routes) > 0
	if !tagged && !hasPayload && !hasMTUOverride {
		return true
	}
	return dev.InterfaceName == "" && dev.InterfaceRef == "" && !hasPayload
}

// destinationRoutes collects the static routes contributed by the Destinations
// an L2A selects: each prefix routed via the next hop of its own address family.
// It is node-independent. The result is sorted and de-duplicated so the rendered
// netplan YAML is stable across reconciles regardless of Kubernetes list order.
// An invalid destinations selector is returned as an error so the caller can
// surface the misconfiguration instead of silently dropping routes.
func destinationRoutes(l2a *nc.Layer2Attachment, data *resolver.ResolvedData) ([]NetplanRoute, error) {
	if l2a.Spec.Destinations == nil {
		return nil, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(l2a.Spec.Destinations)
	if err != nil {
		return nil, fmt.Errorf("invalid destinations selector: %w", err)
	}
	seen := make(map[NetplanRoute]struct{})
	var routes []NetplanRoute
	for i := range data.RawDestinations {
		destination := &data.RawDestinations[i]
		if !selector.Matches(labels.Set(destination.Labels)) {
			continue
		}
		resolved, ok := data.Destinations[destination.Name]
		if !ok || resolved.Spec.NextHop == nil {
			continue
		}
		v4, v6 := validNextHops(resolved.Spec.NextHop)
		for _, prefix := range resolved.Spec.Prefixes {
			route, ok := prefixRoute(prefix, v4, v6)
			if !ok {
				continue
			}
			if _, dup := seen[route]; dup {
				continue
			}
			seen[route] = struct{}{}
			routes = append(routes, route)
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].To != routes[j].To {
			return routes[i].To < routes[j].To
		}
		return routes[i].Via < routes[j].Via
	})
	return routes, nil
}

// validNextHops returns the syntactically valid IPv4 and IPv6 next-hop
// addresses from a NextHopConfig. The CRD does not enforce an IP format, so a
// value that is not a valid address of its declared family is dropped (returned
// as "").
func validNextHops(nh *nc.NextHopConfig) (v4, v6 string) {
	if nh.IPv4 != nil {
		if ip := stdnet.ParseIP(*nh.IPv4); ip != nil && ip.To4() != nil {
			v4 = *nh.IPv4
		}
	}
	if nh.IPv6 != nil {
		if ip := stdnet.ParseIP(*nh.IPv6); ip != nil && ip.To4() == nil {
			v6 = *nh.IPv6
		}
	}
	return v4, v6
}

// prefixRoute builds a route for a prefix via the next hop of its own address
// family. It returns ok=false for an invalid CIDR or when no next hop of the
// matching family is available.
func prefixRoute(prefix, v4, v6 string) (NetplanRoute, bool) {
	_, ipNet, err := stdnet.ParseCIDR(prefix)
	if err != nil {
		return NetplanRoute{}, false
	}
	via := v4
	if ipNet.IP.To4() == nil {
		via = v6
	}
	if via == "" {
		return NetplanRoute{}, false
	}
	return NetplanRoute{To: prefix, Via: via}, true
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
		gwIP, prefixLen := ipmath.ParseCIDRParts(nw.Spec.IPv4.CIDR)
		if gwIP != "" {
			for _, ip := range alloc.IPv4 {
				result.Addresses = append(result.Addresses, fmt.Sprintf("%s/%s", ip, prefixLen))
			}
			result.Gateways = append(result.Gateways, gwIP)
		}
	}

	if nw.Spec.IPv6 != nil && len(alloc.IPv6) > 0 {
		gwIP, prefixLen := ipmath.ParseCIDRParts(nw.Spec.IPv6.CIDR)
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
