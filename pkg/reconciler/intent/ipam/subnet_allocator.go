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
	"fmt"
	"net"
	"sort"
)

// SubnetAllocateResult is the outcome of a SubnetAllocator.Reconcile call.
type SubnetAllocateResult struct {
	// Updated is the new node-to-address map. It preserves every entry from
	// the input map for nodes that are still in scope, drops entries for
	// nodes no longer in scope, and adds entries for newly in-scope nodes.
	Updated map[string]string

	// Removed lists node names whose previous allocation was dropped because
	// the node is no longer in scope.
	Removed []string

	// Unallocated lists node names that are in scope but for which no
	// address could be allocated (subnet exhausted). These nodes appear
	// neither in Updated nor Removed; callers should report a degraded
	// condition.
	Unallocated []string
}

// AllocateSubnet computes a per-node address map for the given CIDR.
//
// Existing entries in `existing` for nodes still in scope are preserved
// verbatim — even if their address falls outside the (possibly changed)
// CIDR, on the assumption that an operator changing the CIDR will tear
// down and re-create the Collector. New entries for newly in-scope nodes
// are picked from the lowest unused host address in the CIDR. The
// allocator skips the network and broadcast addresses for IPv4 subnets;
// for IPv6 it skips the all-zeros host address.
//
// The function is deterministic: nodes are processed in lexical order so
// the resulting allocation depends only on inputs, not on map iteration
// order.
func AllocateSubnet(cidr string, nodeNames []string, existing map[string]string) (*SubnetAllocateResult, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("parsing subnet CIDR %q: %w", cidr, err)
	}

	inScope := make(map[string]struct{}, len(nodeNames))
	for _, n := range nodeNames {
		inScope[n] = struct{}{}
	}

	result := &SubnetAllocateResult{Updated: make(map[string]string, len(nodeNames))}
	used := make(map[string]struct{}, len(existing))

	// 1. Preserve existing allocations for in-scope nodes; record removals.
	for node, addr := range existing {
		if _, ok := inScope[node]; !ok {
			result.Removed = append(result.Removed, node)
			continue
		}
		result.Updated[node] = addr
		used[addr] = struct{}{}
	}
	sort.Strings(result.Removed)

	// 2. Allocate addresses for new in-scope nodes in lexical order.
	sortedNodes := append([]string(nil), nodeNames...)
	sort.Strings(sortedNodes)

	bcast := broadcastAddr(ipNet)
	cursor := nextAddr(ipNet.IP) // skip network address

	for _, node := range sortedNodes {
		if _, ok := result.Updated[node]; ok {
			continue
		}
		// Find next free address.
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
			result.Unallocated = append(result.Unallocated, node)
			continue
		}
		addr := cursor.String()
		result.Updated[node] = addr
		used[addr] = struct{}{}
		cursor = nextAddr(cursor)
	}
	sort.Strings(result.Unallocated)

	return result, nil
}
