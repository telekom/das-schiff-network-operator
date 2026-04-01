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
	"fmt"
	"net"

	"github.com/go-logr/logr"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	broadcast net.IP // nil for IPv6
}

// ReconcileAllocations checks Inbound/Outbound resources with count mode
// and allocates IPs from their referenced Network's CIDR.
// It also allocates per-node IPs for Layer2Attachments with nodeIPs.enabled.
// Allocated IPs are written to the resource's status.addresses field.
func (a *Allocator) ReconcileAllocations(ctx context.Context, fetched *resolver.FetchedResources, networks map[string]*resolver.ResolvedNetwork) error {
	pools := make(map[string]*networkPool)

	// Seed pools with already-allocated addresses to avoid duplicate allocations.
	a.seedExistingAllocations(fetched, pools, networks)

	for i := range fetched.Inbounds {
		inb := &fetched.Inbounds[i]
		if inb.Spec.Count == nil || inb.Spec.Addresses != nil {
			continue
		}
		if inb.Status.Addresses != nil && len(inb.Status.Addresses.IPv4)+len(inb.Status.Addresses.IPv6) > 0 {
			continue // already allocated
		}
		addrs, err := a.allocate(inb.Spec.NetworkRef, int(*inb.Spec.Count), networks, pools)
		if err != nil {
			a.logger.Error(err, "IPAM allocation failed for Inbound", "inbound", inb.Name)
			continue
		}
		inb.Status.Addresses = addrs
		if err := a.client.Status().Update(ctx, inb); err != nil {
			return fmt.Errorf("updating Inbound %q status with allocated addresses: %w", inb.Name, err)
		}
		a.logger.Info("allocated addresses for Inbound", "inbound", inb.Name, "addresses", addrs)
	}

	for i := range fetched.Outbounds {
		outb := &fetched.Outbounds[i]
		if outb.Spec.Count == nil || outb.Spec.Addresses != nil {
			continue
		}
		if outb.Status.Addresses != nil && len(outb.Status.Addresses.IPv4)+len(outb.Status.Addresses.IPv6) > 0 {
			continue // already allocated
		}
		addrs, err := a.allocate(outb.Spec.NetworkRef, int(*outb.Spec.Count), networks, pools)
		if err != nil {
			a.logger.Error(err, "IPAM allocation failed for Outbound", "outbound", outb.Name)
			continue
		}
		outb.Status.Addresses = addrs
		if err := a.client.Status().Update(ctx, outb); err != nil {
			return fmt.Errorf("updating Outbound %q status with allocated addresses: %w", outb.Name, err)
		}
		a.logger.Info("allocated addresses for Outbound", "outbound", outb.Name, "addresses", addrs)
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

// seedExistingAllocations scans resources with existing status.addresses and
// advances pool cursors past the highest already-allocated IP per network CIDR.
func (a *Allocator) seedExistingAllocations(fetched *resolver.FetchedResources, pools map[string]*networkPool, networks map[string]*resolver.ResolvedNetwork) {
	collectAddresses := func(networkRef string, addrs *nc.AddressAllocation) {
		if addrs == nil {
			return
		}
		netObj, ok := networks[networkRef]
		if !ok {
			return
		}
		seedPoolFromAddresses(networkRef+"/v4", netObj.Spec.IPv4, addrs.IPv4, pools)
		seedPoolFromAddresses(networkRef+"/v6", netObj.Spec.IPv6, addrs.IPv6, pools)
	}

	for i := range fetched.Inbounds {
		collectAddresses(fetched.Inbounds[i].Spec.NetworkRef, fetched.Inbounds[i].Status.Addresses)
	}
	for i := range fetched.Outbounds {
		collectAddresses(fetched.Outbounds[i].Spec.NetworkRef, fetched.Outbounds[i].Status.Addresses)
	}
	// Seed from L2A per-node allocations.
	for i := range fetched.Layer2Attachments {
		l2a := &fetched.Layer2Attachments[i]
		for _, addrs := range l2a.Status.NodeAddresses {
			collectAddresses(l2a.Spec.NetworkRef, &addrs)
		}
	}
}

// seedPoolFromAddresses ensures the pool cursor is past any already-allocated IPs.
func seedPoolFromAddresses(poolKey string, ipFamily *nc.IPNetwork, addresses []string, pools map[string]*networkPool) {
	if ipFamily == nil || len(addresses) == 0 {
		return
	}

	pool, ok := pools[poolKey]
	if !ok {
		_, ipNet, err := net.ParseCIDR(ipFamily.CIDR)
		if err != nil {
			return
		}
		pool = &networkPool{
			network:   ipNet,
			nextIP:    nextAddr(ipNet.IP),
			broadcast: broadcastAddr(ipNet),
		}
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
func (a *Allocator) reconcileNodeIPs(ctx context.Context, l2a *nc.Layer2Attachment, nodes []corev1.Node, networks map[string]*resolver.ResolvedNetwork, pools map[string]*networkPool) error {
	if l2a.Status.NodeAddresses == nil {
		l2a.Status.NodeAddresses = make(map[string]nc.AddressAllocation)
	}

	changed := false
	for i := range nodes {
		nodeName := nodes[i].Name
		if _, exists := l2a.Status.NodeAddresses[nodeName]; exists {
			continue // already allocated
		}

		addrs, err := a.allocate(l2a.Spec.NetworkRef, 1, networks, pools)
		if err != nil {
			return fmt.Errorf("allocating node IP for %q on network %q: %w", nodeName, l2a.Spec.NetworkRef, err)
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

// allocate allocates count IPs from the given network's CIDR pool.
func (a *Allocator) allocate(networkRef string, count int, networks map[string]*resolver.ResolvedNetwork, pools map[string]*networkPool) (*nc.AddressAllocation, error) {
	net, ok := networks[networkRef]
	if !ok {
		return nil, fmt.Errorf("network %q not found", networkRef)
	}

	alloc := &nc.AddressAllocation{}

	if net.Spec.IPv4 != nil {
		ips, err := allocateFromCIDR(networkRef+"/v4", net.Spec.IPv4.CIDR, count, pools)
		if err != nil {
			return nil, fmt.Errorf("IPv4 allocation from network %q: %w", networkRef, err)
		}
		alloc.IPv4 = ips
	}

	if net.Spec.IPv6 != nil {
		ips, err := allocateFromCIDR(networkRef+"/v6", net.Spec.IPv6.CIDR, count, pools)
		if err != nil {
			return nil, fmt.Errorf("IPv6 allocation from network %q: %w", networkRef, err)
		}
		alloc.IPv6 = ips
	}

	return alloc, nil
}

// allocateFromCIDR sequentially allocates count IPs from a CIDR, skipping network and broadcast addresses.
func allocateFromCIDR(poolKey, cidr string, count int, pools map[string]*networkPool) ([]string, error) {
	pool, ok := pools[poolKey]
	if !ok {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("parsing CIDR %q: %w", cidr, err)
		}
		// Start allocation from the first host address (skip network address).
		firstHost := nextAddr(ipNet.IP)
		pool = &networkPool{
			network:   ipNet,
			nextIP:    firstHost,
			broadcast: broadcastAddr(ipNet),
		}
		pools[poolKey] = pool
	}

	result := make([]string, 0, count)
	for range count {
		// Skip broadcast address for IPv4 subnets.
		if pool.broadcast != nil && pool.nextIP.Equal(pool.broadcast) {
			return nil, fmt.Errorf("CIDR %q exhausted (reached broadcast)", cidr)
		}
		if !pool.network.Contains(pool.nextIP) {
			return nil, fmt.Errorf("CIDR %q exhausted", cidr)
		}
		result = append(result, pool.nextIP.String())
		pool.nextIP = nextAddr(pool.nextIP)
	}
	return result, nil
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
