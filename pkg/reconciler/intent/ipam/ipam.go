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

package ipam

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// Allocator handles IP address allocation from Network pools.
type Allocator struct {
	client client.Client
	logger logr.Logger
}

// NewAllocator creates a new IPAM Allocator.
func NewAllocator(c client.Client, logger logr.Logger) *Allocator {
	return &Allocator{
		client: c,
		logger: logger.WithName("ipam-allocator"),
	}
}

// networkPool tracks sequential allocation state for a single Network CIDR.
type networkPool struct {
	network   *net.IPNet
	nextIP    net.IP
	broadcast net.IP       // nil for IPv6 and for L3 routing pools
	gateway   net.IP       // anycast gateway to reserve on L2 subnets; nil for L3 pools
	reserved  []*net.IPNet // ranges reserved for pod use, never allocated to nodes
}

// newPool creates an allocation pool for a CIDR with the correct addressing
// semantics. The ip argument is the host part of the authored CIDR (e.g.
// 10.0.1.1 for "10.0.1.1/24"); on L2 subnets it is the anycast gateway shared by
// the IRB and must not be handed out to a node.
//
// For L3 routing pools (Inbound/Outbound, whose IPs are advertised as individual
// routed /32s) every address in the CIDR is usable, including the network and
// broadcast addresses, and there is no gateway to reserve. For L2 subnets
// (Layer2Attachment node IPs on a real VLAN) the network, broadcast and gateway
// addresses are reserved.
func newPool(ip net.IP, ipNet *net.IPNet, l3 bool) *networkPool {
	if l3 {
		return &networkPool{
			network:   ipNet,
			nextIP:    cloneIP(ipNet.IP),
			broadcast: nil,
			gateway:   nil,
		}
	}
	return &networkPool{
		network:   ipNet,
		nextIP:    nextAddr(ipNet.IP),
		broadcast: broadcastAddr(ipNet),
		gateway:   cloneIP(ip),
	}
}

// ReconcileAllocations checks Inbound/Outbound resources with count mode
// and allocates IPs from their referenced Network's CIDR.
// It also allocates per-node IPs for Layer2Attachments with nodeIPs.enabled.
// Allocated IPs are written to the resource's status.addresses field.
func (a *Allocator) ReconcileAllocations(ctx context.Context, fetched *resolver.FetchedResources, networks map[string]*resolver.ResolvedNetwork) error {
	pools := make(map[string]*networkPool)

	// Seed pools with already-allocated addresses to avoid duplicate allocations.
	a.seedExistingAllocations(fetched, pools, networks)

	if err := a.reconcileInboundAllocations(ctx, fetched, networks, pools); err != nil {
		return err
	}
	if err := a.reconcileOutboundAllocations(ctx, fetched, networks, pools); err != nil {
		return err
	}

	// Allocate per-node IPs for Layer2Attachments with nodeIPs.enabled.
	for i := range fetched.Layer2Attachments {
		l2a := &fetched.Layer2Attachments[i]
		if l2a.Spec.NodeIPs == nil || !l2a.Spec.NodeIPs.Enabled {
			continue
		}
		if err := a.reconcileNodeIPs(ctx, l2a, fetched.Nodes, networks, pools); err != nil {
			a.logger.Error(err, "node IP allocation failed for Layer2Attachment", "l2a", l2a.Name)
		}
	}

	return nil
}

func (a *Allocator) reconcileInboundAllocations(ctx context.Context, fetched *resolver.FetchedResources, networks map[string]*resolver.ResolvedNetwork, pools map[string]*networkPool) error {
	for i := range fetched.Inbounds {
		inb := &fetched.Inbounds[i]
		if inb.Spec.Count == nil || inb.Spec.Addresses != nil {
			continue
		}
		if inb.Status.Addresses != nil && len(inb.Status.Addresses.IPv4)+len(inb.Status.Addresses.IPv6) > 0 {
			continue
		}
		addrs, err := a.allocate(inb.Spec.NetworkRef, int(*inb.Spec.Count), networks, pools, true, nil)
		if addrs == nil {
			a.logger.Error(err, "IPAM allocation failed for Inbound", "inbound", inb.Name)
			continue
		}
		if err != nil {
			a.logger.Error(err, "IPAM allocation partially failed for Inbound", "inbound", inb.Name)
		}
		if len(addrs.IPv4)+len(addrs.IPv6) == 0 {
			continue
		}
		inb.Status.Addresses = addrs
		if err := a.client.Status().Update(ctx, inb); err != nil {
			return fmt.Errorf("updating Inbound %q status with allocated addresses: %w", inb.Name, err)
		}
		a.logger.Info("allocated addresses for Inbound", "inbound", inb.Name, "addresses", addrs)
	}
	return nil
}

func (a *Allocator) reconcileOutboundAllocations(ctx context.Context, fetched *resolver.FetchedResources, networks map[string]*resolver.ResolvedNetwork, pools map[string]*networkPool) error {
	for i := range fetched.Outbounds {
		outb := &fetched.Outbounds[i]
		if outb.Spec.Count == nil || outb.Spec.Addresses != nil {
			continue
		}
		if outb.Status.Addresses != nil && len(outb.Status.Addresses.IPv4)+len(outb.Status.Addresses.IPv6) > 0 {
			continue
		}
		addrs, err := a.allocate(outb.Spec.NetworkRef, int(*outb.Spec.Count), networks, pools, true, nil)
		if addrs == nil {
			a.logger.Error(err, "IPAM allocation failed for Outbound", "outbound", outb.Name)
			continue
		}
		if err != nil {
			a.logger.Error(err, "IPAM allocation partially failed for Outbound", "outbound", outb.Name)
		}
		if len(addrs.IPv4)+len(addrs.IPv6) == 0 {
			continue
		}
		outb.Status.Addresses = addrs
		if err := a.client.Status().Update(ctx, outb); err != nil {
			return fmt.Errorf("updating Outbound %q status with allocated addresses: %w", outb.Name, err)
		}
		a.logger.Info("allocated addresses for Outbound", "outbound", outb.Name, "addresses", addrs)
	}
	return nil
}

// seedExistingAllocations scans resources with existing status.addresses and
// advances pool cursors past the highest already-allocated IP per network CIDR.
// Pools are created with the same L2/L3 semantics that the live allocation path
// uses for that consumer, so a routed (L3) pool keeps the network and broadcast
// addresses usable across reconciles.
func (*Allocator) seedExistingAllocations(fetched *resolver.FetchedResources, pools map[string]*networkPool, networks map[string]*resolver.ResolvedNetwork) {
	collectAddresses := func(networkRef string, addrs *nc.AddressAllocation, l3 bool) {
		if addrs == nil {
			return
		}
		netObj, ok := networks[networkRef]
		if !ok {
			return
		}
		seedPoolFromAddresses(networkRef+"/v4", netObj.Spec.IPv4, addrs.IPv4, pools, l3)
		seedPoolFromAddresses(networkRef+"/v6", netObj.Spec.IPv6, addrs.IPv6, pools, l3)
	}

	// Seed L3 routing pools (Inbound/Outbound) first so a Network shared with an
	// L2 attachment keeps routed semantics for its already-allocated addresses.
	for i := range fetched.Inbounds {
		collectAddresses(fetched.Inbounds[i].Spec.NetworkRef, fetched.Inbounds[i].Status.Addresses, true)
	}
	for i := range fetched.Outbounds {
		collectAddresses(fetched.Outbounds[i].Spec.NetworkRef, fetched.Outbounds[i].Status.Addresses, true)
	}
	// Seed from L2A per-node allocations (L2 subnet semantics).
	for i := range fetched.Layer2Attachments {
		l2a := &fetched.Layer2Attachments[i]
		for _, addrs := range l2a.Status.NodeAddresses {
			collectAddresses(l2a.Spec.NetworkRef, &addrs, false)
		}
	}
}

// seedPoolFromAddresses ensures the pool cursor is past any already-allocated IPs.
func seedPoolFromAddresses(poolKey string, ipFamily *nc.IPNetwork, addresses []string, pools map[string]*networkPool, l3 bool) {
	if ipFamily == nil || len(addresses) == 0 {
		return
	}

	pool, ok := pools[poolKey]
	if !ok {
		ip, ipNet, err := net.ParseCIDR(ipFamily.CIDR)
		if err != nil {
			return
		}
		pool = newPool(ip, ipNet, l3)
		pools[poolKey] = pool
	}

	for _, addrStr := range addresses {
		ip := net.ParseIP(addrStr)
		if ip == nil {
			continue
		}
		// Advance pool cursor past this IP if it's >= current nextIP.
		candidateNext := nextAddr(ip)
		if compareIPs(candidateNext, pool.nextIP) > 0 {
			pool.nextIP = candidateNext
		}
	}
}

// compareIPs compares two IP addresses. Returns -1, 0, or 1.
func compareIPs(a, b net.IP) int {
	aN := a.To16()
	bN := b.To16()
	if aN == nil || bN == nil {
		return 0
	}
	for i := range aN {
		if aN[i] < bN[i] {
			return -1
		}
		if aN[i] > bN[i] {
			return 1
		}
	}
	return 0
}

// reconcileNodeIPs allocates one IP per node for an L2A with nodeIPs.enabled.
// Existing allocations are preserved; new nodes get the next available IP.
// IPs that fall within nodeIPs.reservedRanges (reserved for pod use) are skipped.
func (a *Allocator) reconcileNodeIPs(ctx context.Context, l2a *nc.Layer2Attachment, nodes []corev1.Node, networks map[string]*resolver.ResolvedNetwork, pools map[string]*networkPool) error {
	if l2a.Status.NodeAddresses == nil {
		l2a.Status.NodeAddresses = make(map[string]nc.AddressAllocation)
	}

	var reserved []*net.IPNet
	if l2a.Spec.NodeIPs != nil {
		parsed, err := parseReservedRanges(l2a.Spec.NodeIPs.ReservedRanges)
		if err != nil {
			return fmt.Errorf("parsing reservedRanges for Layer2Attachment %q: %w", l2a.Name, err)
		}
		reserved = parsed
	}

	changed := false
	for i := range nodes {
		nodeName := nodes[i].Name
		if _, exists := l2a.Status.NodeAddresses[nodeName]; exists {
			continue // already allocated
		}

		addrs, err := a.allocate(l2a.Spec.NetworkRef, 1, networks, pools, false, reserved)
		if addrs == nil {
			// Hard failure (e.g. network not resolved): abort this L2A.
			return fmt.Errorf("allocating node IP for %q on network %q: %w", nodeName, l2a.Spec.NetworkRef, err)
		}
		if err != nil {
			a.logger.Error(err, "node IP allocation partially failed", "l2a", l2a.Name, "node", nodeName)
		}
		if len(addrs.IPv4)+len(addrs.IPv6) == 0 {
			continue
		}
		l2a.Status.NodeAddresses[nodeName] = *addrs
		changed = true
		a.logger.Info("allocated node IP", "l2a", l2a.Name, "node", nodeName, "addresses", addrs)
	}

	if changed {
		if err := a.client.Status().Update(ctx, l2a); err != nil {
			return fmt.Errorf("updating Layer2Attachment %q status: %w", l2a.Name, err)
		}
	}
	return nil
}

// parseReservedRanges parses a list of CIDR strings into IPNets, rejecting any
// malformed entry so a misconfigured reservation fails loudly rather than being
// silently ignored.
func parseReservedRanges(ranges []string) ([]*net.IPNet, error) {
	if len(ranges) == 0 {
		return nil, nil
	}
	out := make([]*net.IPNet, 0, len(ranges))
	for _, r := range ranges {
		_, ipNet, err := net.ParseCIDR(r)
		if err != nil {
			return nil, fmt.Errorf("invalid reserved range %q: %w", r, err)
		}
		out = append(out, ipNet)
	}
	return out, nil
}

// allocate allocates count IPs from the given network's CIDR pool.
//
// l3 selects the addressing semantics. For L3 routing pools (Inbound/Outbound
// egress, whose IPs are advertised as individual routed /32s) every address in
// the CIDR is usable, including the network and broadcast addresses. For L2
// subnets (Layer2Attachment node IPs on a real VLAN) the network and broadcast
// addresses are reserved. reserved lists CIDR ranges (typically pod ranges) that
// must never be handed out; it is nil for L3 consumers.
//
// Allocation is per-family and non-atomic: if one address family is exhausted
// the other family's addresses are still returned, and the per-family failure is
// reported via the returned error. This prevents one family's exhaustion from
// blocking exposure of the other. A nil allocation is returned only when the
// network itself cannot be resolved.
func (*Allocator) allocate(networkRef string, count int, networks map[string]*resolver.ResolvedNetwork, pools map[string]*networkPool, l3 bool, reserved []*net.IPNet) (*nc.AddressAllocation, error) {
	netObj, ok := networks[networkRef]
	if !ok {
		return nil, fmt.Errorf("network %q not found", networkRef)
	}

	alloc := &nc.AddressAllocation{}
	var errs []error

	if netObj.Spec.IPv4 != nil {
		ips, err := allocateFromCIDR(networkRef+"/v4", netObj.Spec.IPv4.CIDR, count, pools, l3, reserved)
		if err != nil {
			errs = append(errs, fmt.Errorf("IPv4 allocation from network %q: %w", networkRef, err))
		} else {
			alloc.IPv4 = ips
		}
	}

	if netObj.Spec.IPv6 != nil {
		ips, err := allocateFromCIDR(networkRef+"/v6", netObj.Spec.IPv6.CIDR, count, pools, l3, reserved)
		if err != nil {
			errs = append(errs, fmt.Errorf("IPv6 allocation from network %q: %w", networkRef, err))
		} else {
			alloc.IPv6 = ips
		}
	}

	return alloc, errors.Join(errs...)
}

// allocateFromCIDR sequentially allocates count IPs from a CIDR. For L2 subnets
// it skips the network and broadcast addresses; for L3 routing pools every
// address in the CIDR is usable.
func allocateFromCIDR(poolKey, cidr string, count int, pools map[string]*networkPool, l3 bool, reserved []*net.IPNet) ([]string, error) {
	pool, ok := pools[poolKey]
	if !ok {
		ip, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("parsing CIDR %q: %w", cidr, err)
		}
		pool = newPool(ip, ipNet, l3)
		pools[poolKey] = pool
	}
	pool.reserved = mergeReserved(pool.reserved, reserved)

	result := make([]string, 0, count)
	for range count {
		// Advance to the first usable address, skipping the anycast gateway (owned
		// by the IRB on L2 subnets) and any ranges reserved for pod use. Handing
		// out either would create a duplicate IP on the shared segment.
		for {
			// Skip broadcast address for IPv4 subnets.
			if pool.broadcast != nil && pool.nextIP.Equal(pool.broadcast) {
				return nil, fmt.Errorf("CIDR %q exhausted (reached broadcast)", cidr)
			}
			if !pool.network.Contains(pool.nextIP) {
				return nil, fmt.Errorf("CIDR %q exhausted", cidr)
			}
			if (pool.gateway != nil && pool.nextIP.Equal(pool.gateway)) || inReserved(pool.reserved, pool.nextIP) {
				pool.nextIP = nextAddr(pool.nextIP)
				continue
			}
			break
		}
		result = append(result, pool.nextIP.String())
		pool.nextIP = nextAddr(pool.nextIP)
	}
	return result, nil
}

// inReserved reports whether ip falls within any of the reserved ranges.
func inReserved(reserved []*net.IPNet, ip net.IP) bool {
	for _, r := range reserved {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// mergeReserved unions two sets of reserved ranges, deduplicating by CIDR string
// so repeated allocations on the same pool don't grow the set unbounded.
func mergeReserved(existing, add []*net.IPNet) []*net.IPNet {
	if len(add) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(add))
	for _, r := range existing {
		seen[r.String()] = struct{}{}
	}
	out := existing
	for _, r := range add {
		if _, ok := seen[r.String()]; ok {
			continue
		}
		seen[r.String()] = struct{}{}
		out = append(out, r)
	}
	return out
}

// cloneIP returns a copy of ip to avoid aliasing the parsed CIDR's address.
func cloneIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	return out
}

// broadcastAddr computes the broadcast address for an IPv4 subnet.
// Returns nil for IPv6 (no broadcast concept) or /32 prefixes.
func broadcastAddr(ipNet *net.IPNet) net.IP {
	ip := ipNet.IP.To4()
	if ip == nil {
		return nil // IPv6 — no broadcast
	}
	mask := ipNet.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:] // convert to 4-byte mask
	}
	ones, bits := ipNet.Mask.Size()
	if ones == bits {
		return nil // /32 — no broadcast
	}
	bcast := make(net.IP, len(ip))
	for i := range ip {
		bcast[i] = ip[i] | ^mask[i]
	}
	return bcast
}

// nextAddr increments an IP address by one.
func nextAddr(ip net.IP) net.IP {
	next := make(net.IP, len(ip))
	copy(next, ip)

	if len(next) == net.IPv4len || len(next) == net.IPv6len {
		// Treat as big-endian integer and increment.
		if len(next) == net.IPv4len {
			val := binary.BigEndian.Uint32(next)
			val++
			binary.BigEndian.PutUint32(next, val)
		} else {
			// For IPv6, increment the lower 64 bits first.
			lo := binary.BigEndian.Uint64(next[8:])
			lo++
			binary.BigEndian.PutUint64(next[8:], lo)
			if lo == 0 { // overflow into upper 64 bits
				hi := binary.BigEndian.Uint64(next[:8])
				hi++
				binary.BigEndian.PutUint64(next[:8], hi)
			}
		}
		return next
	}

	// Fallback: simple byte-level increment from the end.
	for j := len(next) - 1; j >= 0; j-- {
		next[j]++
		if next[j] > 0 {
			break
		}
	}
	return next
}
