package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

const (
	// sourceKindLayer2 is the MirrorSource Kind for a Layer2NetworkConfiguration.
	sourceKindLayer2 = "Layer2NetworkConfiguration"
	// sourceKindVRF is the MirrorSource Kind for a VRFRouteConfiguration.
	sourceKindVRF = "VRFRouteConfiguration"
	// targetKind is the Kind a MirrorTarget reference must use.
	targetKind = "MirrorTarget"
	// mirrorAPIGroup is the apiGroup mirror references must point at (empty is
	// also accepted as "this group").
	mirrorAPIGroup = "network.t-caas.telekom.com"
)

// refAPIGroupOK reports whether a reference's apiGroup is empty (defaulted) or the
// operator's own group; references into foreign groups are rejected.
func refAPIGroupOK(ref corev1.TypedObjectReference) bool {
	return ref.APIGroup == nil || *ref.APIGroup == "" || *ref.APIGroup == mirrorAPIGroup
}

// validMirrorTargetRef reports whether a MirrorTarget reference has the right kind
// and apiGroup.
func validMirrorTargetRef(ref corev1.TypedObjectReference) bool {
	return ref.Kind == targetKind && refAPIGroupOK(ref)
}

// buildNodeMirror resolves the MirrorSelector/MirrorTarget snapshots from the
// revision and injects the resulting GRE tunnels, per-node source loopbacks, EVPN
// export-filter entries and MirrorACLs into the node's NodeNetworkConfig.
//
// Mirror data is sourced from the NetworkConfigRevision (not live CRDs), so that
// mirror changes bump the revision hash and roll out through the normal gated
// pipeline. The mirror VRF and its loopback definition (name + subnet) live on the
// VRFRouteConfiguration and are already snapshotted in the revision; only the
// per-node IP, GRE interface and ACL entries are injected here.
func (crr *ConfigRevisionReconciler) buildNodeMirror(ctx context.Context, node *corev1.Node, revision *v1alpha1.NetworkConfigRevision, c *v1alpha1.NodeNetworkConfig) error {
	if len(revision.Spec.MirrorSelectors) == 0 {
		return nil
	}

	targets := make(map[string]*v1alpha1.MirrorTargetRevision, len(revision.Spec.MirrorTargets))
	for i := range revision.Spec.MirrorTargets {
		targets[revision.Spec.MirrorTargets[i].Name] = &revision.Spec.MirrorTargets[i]
	}

	alloc, err := crr.mirrorAllocator(ctx, revision)
	if err != nil {
		return err
	}

	// Track GRE interfaces already created on this node (keyed by target name) so
	// multiple selectors referencing the same target share a single tunnel.
	createdTargets := map[string]string{}

	// Selectors are already sorted by name in the revision snapshot.
	for i := range revision.Spec.MirrorSelectors {
		applyMirrorSelector(node, revision, &revision.Spec.MirrorSelectors[i], targets, alloc, createdTargets, c)
	}

	return nil
}

// mirrorAllocator returns the per-node loopback allocator for the revision and
// the current ready-node set, recomputing it (and re-listing configs) only when
// either changes. Keying on the ready-node set ensures a node that joins during a
// revision's lifetime is allocated its loopback immediately instead of being
// silently skipped until the next revision bump.
func (crr *ConfigRevisionReconciler) mirrorAllocator(ctx context.Context, revision *v1alpha1.NetworkConfigRevision) (*loopbackAllocator, error) {
	nodeNames, err := crr.listReadyNodeNames(ctx)
	if err != nil {
		return nil, err
	}

	// nodeNames is sorted by listReadyNodeNames, so this key is stable.
	key := revision.Spec.Revision + "|" + strings.Join(nodeNames, ",")
	if crr.mirrorAllocCache.alloc != nil && crr.mirrorAllocCache.key == key {
		return crr.mirrorAllocCache.alloc, nil
	}

	existingConfigs, err := crr.listConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("error listing NodeNetworkConfigs for mirror allocation: %w", err)
	}

	alloc := newLoopbackAllocator(revision, existingConfigs.Items, nodeNames)
	crr.mirrorAllocCache = mirrorAllocCache{key: key, alloc: alloc}
	return alloc, nil
}

// applyMirrorSelector resolves a single MirrorSelector for the given node and, when
// applicable, injects the GRE tunnel, loopback, export-filter entry and MirrorACL.
// Selectors whose source or mirror VRF are not present on the node are skipped.
func applyMirrorSelector(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision, sel *v1alpha1.MirrorSelectorRevision, targets map[string]*v1alpha1.MirrorTargetRevision, alloc *loopbackAllocator, createdTargets map[string]string, c *v1alpha1.NodeNetworkConfig) {
	if !validMirrorTargetRef(sel.MirrorTarget) {
		// Reference into a foreign kind/apiGroup: ignore. Status reporting handles this.
		return
	}
	target, ok := targets[sel.MirrorTarget.Name]
	if !ok {
		// Unresolvable target: nothing to inject. Status reporting handles this.
		return
	}

	// Skip before injecting any tunnel/loopback when the selector's source is not
	// present on this node — otherwise the node would get a mirror VRF loopback and
	// GRE tunnel with no ACL attached (wasted config, and it would inflate
	// MirrorTarget.Status.ActiveNodes).
	if !mirrorSourcePresent(sel, revision, c) {
		return
	}

	greName := ensureMirrorTunnel(node, target, alloc, createdTargets, c)
	if greName == "" {
		// Mirror VRF or loopback not available on this node - skip.
		return
	}

	acl := v1alpha1.MirrorACL{
		TrafficMatch:      sel.TrafficMatch,
		MirrorDestination: greName,
		Direction:         sel.Direction,
	}

	attachMirrorACL(sel, revision, &acl, c)
}

// mirrorSourcePresent reports whether the selector's MirrorSource (a Layer2 or a
// fabric VRF) is configured on this node and the reference is valid.
func mirrorSourcePresent(sel *v1alpha1.MirrorSelectorRevision, revision *v1alpha1.NetworkConfigRevision, c *v1alpha1.NodeNetworkConfig) bool {
	if !refAPIGroupOK(sel.MirrorSource) {
		return false
	}
	switch sel.MirrorSource.Kind {
	case sourceKindLayer2:
		key, ok := layer2KeyForSource(revision, sel.MirrorSource.Name)
		if !ok {
			return false
		}
		_, present := c.Spec.Layer2s[key]
		return present
	case sourceKindVRF:
		vrfName, ok := vrfNameForSource(revision, sel.MirrorSource.Name)
		if !ok {
			return false
		}
		_, present := c.Spec.FabricVRFs[vrfName]
		return present
	}
	return false
}

// ensureMirrorTunnel makes sure the mirror VRF on the node carries the per-node
// loopback and the GRE tunnel for the given target, and that the source IP is
// advertised via the VRF's EVPN export filter. It returns the GRE interface name,
// or an empty string if the mirror VRF/loopback are not available on this node.
func ensureMirrorTunnel(node *corev1.Node, target *v1alpha1.MirrorTargetRevision, alloc *loopbackAllocator, createdTargets map[string]string, c *v1alpha1.NodeNetworkConfig) string {
	if name, done := createdTargets[target.Name]; done {
		return name
	}

	fabricVrf, ok := c.Spec.FabricVRFs[target.DestinationVrf]
	if !ok {
		// The mirror VRF is not configured on this node.
		return ""
	}

	srcIP, ok := alloc.allocate(target.DestinationVrf, target.SourceLoopback, node.Name)
	if !ok {
		// No loopback subnet defined for this VRF/loopback, or subnet exhausted.
		return ""
	}

	hostAddr := hostAddress(srcIP)

	if fabricVrf.Loopbacks == nil {
		fabricVrf.Loopbacks = map[string]v1alpha1.Loopback{}
	}
	fabricVrf.Loopbacks[target.SourceLoopback] = v1alpha1.Loopback{
		IPAddresses: []string{hostAddr},
	}

	greName := greInterfaceName(target.Name, target.Type)
	if fabricVrf.GREs == nil {
		fabricVrf.GREs = map[string]v1alpha1.GRE{}
	}
	fabricVrf.GREs[greName] = v1alpha1.GRE{
		SourceAddress:      srcIP,
		SourceInterface:    target.SourceLoopback,
		DestinationAddress: target.DestinationIP,
		Layer:              greLayer(target.Type),
		EncapsulationKey:   target.TunnelKey,
	}

	appendExportFilterPrefix(&fabricVrf, hostAddr)

	c.Spec.FabricVRFs[target.DestinationVrf] = fabricVrf
	createdTargets[target.Name] = greName
	return greName
}

// attachMirrorACL adds the MirrorACL to the selector's source (a Layer2 or a fabric
// VRF) when that source is present on the node. The source is referenced by object
// name and resolved to the NodeNetworkConfig map key (VLAN ID for Layer2, VRF name
// for VRFRouteConfiguration) via the revision.
func attachMirrorACL(sel *v1alpha1.MirrorSelectorRevision, revision *v1alpha1.NetworkConfigRevision, acl *v1alpha1.MirrorACL, c *v1alpha1.NodeNetworkConfig) {
	if !refAPIGroupOK(sel.MirrorSource) {
		// Reference into a foreign apiGroup: ignore.
		return
	}
	switch sel.MirrorSource.Kind {
	case sourceKindLayer2:
		key, ok := layer2KeyForSource(revision, sel.MirrorSource.Name)
		if !ok {
			return
		}
		if l2, present := c.Spec.Layer2s[key]; present {
			l2.MirrorACLs = append(l2.MirrorACLs, *acl)
			c.Spec.Layer2s[key] = l2
		}
	case sourceKindVRF:
		vrfName, ok := vrfNameForSource(revision, sel.MirrorSource.Name)
		if !ok {
			return
		}
		if fvrf, present := c.Spec.FabricVRFs[vrfName]; present {
			fvrf.MirrorACLs = append(fvrf.MirrorACLs, *acl)
			c.Spec.FabricVRFs[vrfName] = fvrf
		}
	}
}

// layer2KeyForSource resolves a Layer2NetworkConfiguration object name to the
// NodeNetworkConfig Layer2s map key (the VLAN ID as string).
func layer2KeyForSource(revision *v1alpha1.NetworkConfigRevision, name string) (string, bool) {
	for i := range revision.Spec.Layer2 {
		if revision.Spec.Layer2[i].Name == name {
			return fmt.Sprintf("%d", revision.Spec.Layer2[i].ID), true
		}
	}
	return "", false
}

// vrfNameForSource resolves a VRFRouteConfiguration object name to its VRF name
// (the FabricVRFs map key).
func vrfNameForSource(revision *v1alpha1.NetworkConfigRevision, name string) (string, bool) {
	for i := range revision.Spec.Vrf {
		if revision.Spec.Vrf[i].Name == name {
			return revision.Spec.Vrf[i].VRF, true
		}
	}
	return "", false
}

func (crr *ConfigRevisionReconciler) listReadyNodeNames(ctx context.Context) ([]string, error) {
	nodes, err := listNodes(ctx, crr.client)
	if err != nil {
		return nil, fmt.Errorf("error listing nodes for mirror allocation: %w", err)
	}
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// appendExportFilterPrefix appends a permit entry for the given host address to the
// VRF's EVPN export filter, without overwriting any user-defined items.
func appendExportFilterPrefix(fabricVrf *v1alpha1.FabricVRF, hostAddr string) {
	if fabricVrf.EVPNExportFilter == nil {
		// A fabric VRF built by the operator always carries an export filter (see
		// createFabricVRF), so this branch is only a defensive fallback. Default to
		// Accept rather than Reject so we never silently suppress a VRF's other EVPN
		// advertisements just because a mirror source IP had to be permitted.
		fabricVrf.EVPNExportFilter = &v1alpha1.Filter{
			DefaultAction: v1alpha1.Action{Type: v1alpha1.Accept},
		}
	}
	for i := range fabricVrf.EVPNExportFilter.Items {
		if p := fabricVrf.EVPNExportFilter.Items[i].Matcher.Prefix; p != nil && p.Prefix == hostAddr {
			return
		}
	}
	fabricVrf.EVPNExportFilter.Items = append(fabricVrf.EVPNExportFilter.Items, v1alpha1.FilterItem{
		Matcher: v1alpha1.Matcher{
			Prefix: &v1alpha1.PrefixMatcher{Prefix: hostAddr},
		},
		Action: v1alpha1.Action{Type: v1alpha1.Accept},
	})
}

func greLayer(t v1alpha1.MirrorTargetType) v1alpha1.GRELayer {
	if t == v1alpha1.MirrorTargetTypeL2GRE {
		return v1alpha1.GRELayer2
	}
	return v1alpha1.GRELayer3
}

// greInterfaceName returns a deterministic, Linux-safe (<=15 char) interface name
// for the GRE tunnel of a MirrorTarget.
func greInterfaceName(targetName string, t v1alpha1.MirrorTargetType) string {
	sum := sha256.Sum256([]byte(targetName))
	h := hex.EncodeToString(sum[:])[:8]
	if t == v1alpha1.MirrorTargetTypeL2GRE {
		return "gtap-" + h
	}
	return "gre-" + h
}

// hostAddress returns the bare IP with a single-host prefix (/32 for IPv4, /128 for IPv6).
func hostAddress(ip string) string {
	if strings.Contains(ip, ":") {
		return ip + "/128"
	}
	return ip + "/32"
}

// loopbackAllocator computes deterministic per-node loopback IPs from the subnet
// declared on each VRFRouteConfiguration loopback, preserving any IP already present
// in a node's existing NodeNetworkConfig.
type loopbackAllocator struct {
	// subnets maps "<vrf>/<loopback>" to its CIDR (from the revision).
	subnets map[string]string
	// existing maps "<vrf>/<loopback>" to node -> already-allocated bare IP.
	existing map[string]map[string]string
	// nodeNames is the sorted set of all schedulable nodes.
	nodeNames []string
	// cache memoises computed node -> IP maps per "<vrf>/<loopback>".
	cache map[string]map[string]string
}

func newLoopbackAllocator(revision *v1alpha1.NetworkConfigRevision, configs []v1alpha1.NodeNetworkConfig, nodeNames []string) *loopbackAllocator {
	a := &loopbackAllocator{
		subnets:   map[string]string{},
		existing:  map[string]map[string]string{},
		nodeNames: nodeNames,
		cache:     map[string]map[string]string{},
	}

	for i := range revision.Spec.Vrf {
		vrf := &revision.Spec.Vrf[i]
		for j := range vrf.Loopbacks {
			key := loopbackKey(vrf.VRF, vrf.Loopbacks[j].Name)
			a.subnets[key] = vrf.Loopbacks[j].Subnet
		}
	}

	for i := range configs {
		for vrfName := range configs[i].Spec.FabricVRFs {
			fvrf := configs[i].Spec.FabricVRFs[vrfName]
			for lbName, lb := range fvrf.Loopbacks {
				if len(lb.IPAddresses) == 0 {
					continue
				}
				key := loopbackKey(vrfName, lbName)
				if _, ok := a.existing[key]; !ok {
					a.existing[key] = map[string]string{}
				}
				a.existing[key][configs[i].Name] = bareIP(lb.IPAddresses[0])
			}
		}
	}

	return a
}

// allocate returns the bare per-node IP for the given VRF/loopback, computing the
// full allocation lazily and caching it. The second return value is false when no
// subnet is defined or the subnet is exhausted for this node.
func (a *loopbackAllocator) allocate(vrf, loopback, nodeName string) (string, bool) {
	key := loopbackKey(vrf, loopback)
	subnet, ok := a.subnets[key]
	if !ok || subnet == "" {
		return "", false
	}

	m, cached := a.cache[key]
	if !cached {
		result, err := allocateSubnet(subnet, a.nodeNames, a.existing[key])
		if err != nil {
			return "", false
		}
		m = result
		a.cache[key] = m
	}

	ip, ok := m[nodeName]
	return ip, ok
}

func loopbackKey(vrf, loopback string) string {
	return vrf + "/" + loopback
}

func bareIP(addr string) string {
	host, _, _ := strings.Cut(addr, "/")
	return host
}

// allocateSubnet computes a deterministic node -> IP map for a CIDR. Existing
// allocations for in-scope nodes are preserved; new nodes get the lowest free host
// address. Network and broadcast addresses (IPv4) are skipped. Returns a map that
// contains only the nodes that could be allocated an address.
func allocateSubnet(cidr string, nodeNames []string, existing map[string]string) (map[string]string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("parsing loopback subnet %q: %w", cidr, err)
	}

	inScope := make(map[string]struct{}, len(nodeNames))
	for _, n := range nodeNames {
		inScope[n] = struct{}{}
	}

	result := make(map[string]string, len(nodeNames))
	used := make(map[string]struct{}, len(existing))
	bcast := broadcastAddr(ipNet)
	preserveExistingAllocations(existing, inScope, ipNet, bcast, result, used)

	sorted := append([]string(nil), nodeNames...)
	sort.Strings(sorted)

	cursor := nextAddr(ipNet.IP)
	for _, node := range sorted {
		if _, ok := result[node]; ok {
			continue
		}
		for ipNet.Contains(cursor) {
			if bcast != nil && cursor.Equal(bcast) {
				break
			}
			if _, taken := used[cursor.String()]; taken {
				cursor = nextAddr(cursor)
				continue
			}
			break
		}
		if !ipNet.Contains(cursor) || (bcast != nil && cursor.Equal(bcast)) {
			continue
		}
		addr := cursor.String()
		result[node] = addr
		used[addr] = struct{}{}
		cursor = nextAddr(cursor)
	}

	return result, nil
}

// preserveExistingAllocations records the existing per-node allocations. Every
// still-configured in-subnet address is reserved in `used` so it is never handed
// to a different node — even for nodes that are currently out of scope (e.g.
// temporarily NotReady), whose NodeNetworkConfig still carries the address and
// would otherwise collide with a freshly-allocated one. Addresses are only
// re-emitted in `result` for in-scope nodes; out-of-subnet addresses (e.g. after
// a subnet change) are dropped so the node is reallocated a valid address.
func preserveExistingAllocations(existing map[string]string, inScope map[string]struct{}, ipNet *net.IPNet, bcast net.IP, result map[string]string, used map[string]struct{}) {
	for node, addr := range existing {
		if !addrInSubnet(addr, ipNet, bcast) {
			continue
		}
		used[addr] = struct{}{}
		if _, ok := inScope[node]; ok {
			result[node] = addr
		}
	}
}

// addrInSubnet reports whether addr is a usable host address inside ipNet (i.e.
// parseable, contained in the CIDR, and not the network or broadcast address).
func addrInSubnet(addr string, ipNet *net.IPNet, bcast net.IP) bool {
	ip := net.ParseIP(addr)
	if ip == nil || !ipNet.Contains(ip) {
		return false
	}
	if ip.Equal(ipNet.IP) {
		return false
	}
	return bcast == nil || !ip.Equal(bcast)
}

// nextAddr returns the IP numerically following ip.
func nextAddr(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			break
		}
	}
	return out
}

// broadcastAddr returns the broadcast address of an IPv4 network, or nil for IPv6.
func broadcastAddr(ipNet *net.IPNet) net.IP {
	ip4 := ipNet.IP.To4()
	if ip4 == nil {
		return nil
	}
	bcast := make(net.IP, net.IPv4len)
	for i := 0; i < net.IPv4len; i++ {
		bcast[i] = ip4[i] | ^ipNet.Mask[i]
	}
	return bcast
}
