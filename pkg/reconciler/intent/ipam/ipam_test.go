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
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

func TestNextAddr(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"basic IPv4", "10.0.0.1", "10.0.0.2"},
		{"IPv4 rollover byte", "10.0.0.255", "10.0.1.0"},
		{"IPv4 zero", "10.0.0.0", "10.0.0.1"},
		{"IPv6 basic", "fd00::1", "fd00::2"},
		{"IPv6 rollover", "fd00::ffff", "fd00::1:0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.input)
			require.NotNil(t, ip)
			// net.ParseIP returns 16-byte form; for IPv4 tests, convert to 4-byte
			if v4 := ip.To4(); v4 != nil {
				ip = v4
			}
			result := nextAddr(ip)
			assert.Equal(t, tt.expect, result.String())
		})
	}
}

func TestBroadcastAddr(t *testing.T) {
	tests := []struct {
		name   string
		cidr   string
		expect string
	}{
		{"IPv4 /24", "10.0.0.0/24", "10.0.0.255"},
		{"IPv4 /28", "192.168.1.0/28", "192.168.1.15"},
		{"IPv4 /30", "10.0.0.0/30", "10.0.0.3"},
		{"IPv4 /31", "10.0.0.0/31", "10.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ipNet, err := net.ParseCIDR(tt.cidr)
			require.NoError(t, err)
			bcast := broadcastAddr(ipNet)
			require.NotNil(t, bcast)
			assert.Equal(t, tt.expect, bcast.String())
		})
	}

	t.Run("IPv6 returns nil", func(t *testing.T) {
		_, ipNet, err := net.ParseCIDR("fd00::/64")
		require.NoError(t, err)
		assert.Nil(t, broadcastAddr(ipNet))
	})

	t.Run("IPv4 /32 returns nil", func(t *testing.T) {
		_, ipNet, err := net.ParseCIDR("10.0.0.1/32")
		require.NoError(t, err)
		assert.Nil(t, broadcastAddr(ipNet))
	})
}

func TestAllocateFromCIDR(t *testing.T) {
	t.Run("basic sequential allocation", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		ips, err := allocateFromCIDR("test/v4", "10.0.0.0/24", 3, pools, false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, ips)
	})

	t.Run("continues from previous allocation", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		ips1, err := allocateFromCIDR("test/v4", "10.0.0.0/24", 2, pools, false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, ips1)

		ips2, err := allocateFromCIDR("test/v4", "10.0.0.0/24", 2, pools, false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.3", "10.0.0.4"}, ips2)
	})

	t.Run("skips broadcast address", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		// /30 has 4 addresses: .0 (network), .1, .2, .3 (broadcast)
		// Usable: .1, .2 only
		ips, err := allocateFromCIDR("test/v4", "10.0.0.0/30", 2, pools, false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, ips)

		// Third allocation should fail — .3 is broadcast
		_, err = allocateFromCIDR("test/v4", "10.0.0.0/30", 1, pools, false, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "broadcast")
	})

	t.Run("exhaustion", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		// /30 has 2 usable hosts
		_, err := allocateFromCIDR("test/v4", "10.0.0.0/30", 3, pools, false, nil)
		assert.Error(t, err)
	})

	t.Run("L3 pool uses full CIDR range", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		// L3 routing pool: network and broadcast addresses are usable too.
		// /30 yields all 4 addresses.
		ips, err := allocateFromCIDR("test/v4", "10.100.16.236/30", 4, pools, true, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.100.16.236", "10.100.16.237", "10.100.16.238", "10.100.16.239"}, ips)
	})

	t.Run("L3 pool exhaustion past CIDR", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		_, err := allocateFromCIDR("test/v4", "10.0.0.0/30", 5, pools, true, nil)
		assert.Error(t, err)
	})

	t.Run("IPv6 allocation", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		ips, err := allocateFromCIDR("test/v6", "fd00::/120", 2, pools, false, nil)
		require.NoError(t, err)
		assert.Len(t, ips, 2)
		assert.Equal(t, "fd00::1", ips[0])
		assert.Equal(t, "fd00::2", ips[1])
	})

	t.Run("L2 pool reserves the anycast gateway", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		// The Network CIDR's host part (.1) is the IRB anycast gateway; node IP
		// allocation must skip it to avoid a duplicate IP on the segment.
		ips, err := allocateFromCIDR("test/v4", "10.0.1.1/24", 3, pools, false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.1.2", "10.0.1.3", "10.0.1.4"}, ips)
	})

	t.Run("L2 pool reserves a mid-range gateway", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		// Gateway authored mid-range (.3) must be skipped wherever it falls.
		ips, err := allocateFromCIDR("test/v4", "10.0.2.3/29", 4, pools, false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.2.1", "10.0.2.2", "10.0.2.4", "10.0.2.5"}, ips)
	})

	t.Run("L2 IPv6 pool reserves the anycast gateway", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		ips, err := allocateFromCIDR("test/v6", "fd00::1/120", 2, pools, false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"fd00::2", "fd00::3"}, ips)
	})

	t.Run("skips pod-reserved ranges", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		_, reserved, _ := net.ParseCIDR("10.0.0.0/28") // .0-.15 reserved for pods
		ips, err := allocateFromCIDR("test/v4", "10.0.0.0/24", 3, pools, false, []*net.IPNet{reserved})
		require.NoError(t, err)
		// .1 gateway + .1-.15 reserved skipped; allocation starts at .16.
		assert.Equal(t, []string{"10.0.0.16", "10.0.0.17", "10.0.0.18"}, ips)
	})

	t.Run("skips multiple disjoint reserved ranges", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		_, r1, _ := net.ParseCIDR("10.0.0.4/30") // .4-.7
		_, r2, _ := net.ParseCIDR("10.0.0.8/30") // .8-.11
		ips, err := allocateFromCIDR("test/v4", "10.0.0.0/24", 4, pools, false, []*net.IPNet{r1, r2})
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.12"}, ips)
	})

	t.Run("exhaustion when reserved range fills the subnet", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		_, reserved, _ := net.ParseCIDR("10.0.0.0/29") // covers all usable hosts in /29
		_, err := allocateFromCIDR("test/v4", "10.0.0.0/29", 1, pools, false, []*net.IPNet{reserved})
		assert.Error(t, err)
	})
}

func TestCompareIPs(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		expect int
	}{
		{"equal", "10.0.0.1", "10.0.0.1", 0},
		{"a less", "10.0.0.1", "10.0.0.2", -1},
		{"a greater", "10.0.0.5", "10.0.0.2", 1},
		{"cross-byte", "10.0.0.255", "10.0.1.0", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := net.ParseIP(tt.a)
			b := net.ParseIP(tt.b)
			assert.Equal(t, tt.expect, compareIPs(a, b))
		})
	}
}

func TestSeedPoolFromAddresses(t *testing.T) {
	t.Run("seeds past highest allocated address", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		_, ipNet, _ := net.ParseCIDR("10.0.0.0/24")
		pools["net1/v4"] = &networkPool{
			network:   ipNet,
			nextIP:    net.ParseIP("10.0.0.1").To4(),
			broadcast: broadcastAddr(ipNet),
		}

		// Simulate 10.0.0.1 and 10.0.0.3 already allocated
		seedPoolFromAddresses("net1/v4", nil, []string{"10.0.0.1", "10.0.0.3"}, pools, false)
		// nil ipFamily means no-op
		assert.Equal(t, "10.0.0.1", pools["net1/v4"].nextIP.String())
	})

	t.Run("seeded L3 pool keeps routed semantics across reconcile", func(t *testing.T) {
		// Regression: seeding must not downgrade an L3 routing pool to L2
		// semantics. A /30 routed pool already holding .236 should still be able
		// to hand out the network (.236) and broadcast (.239) addresses.
		pools := make(map[string]*networkPool)
		family := &nc.IPNetwork{CIDR: "10.100.16.236/30"}

		// Simulate a prior reconcile having allocated the first routed address.
		seedPoolFromAddresses("net/v4", family, []string{"10.100.16.236"}, pools, true)

		// The pool must not reserve the broadcast address.
		require.NotNil(t, pools["net/v4"])
		assert.Nil(t, pools["net/v4"].broadcast)

		// Allocating the remaining three addresses must succeed (incl. broadcast).
		ips, err := allocateFromCIDR("net/v4", family.CIDR, 3, pools, true, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.100.16.237", "10.100.16.238", "10.100.16.239"}, ips)
	})

	t.Run("seeded L2 pool keeps subnet semantics", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		family := &nc.IPNetwork{CIDR: "10.0.0.0/30"}

		seedPoolFromAddresses("net/v4", family, []string{"10.0.0.1"}, pools, false)

		require.NotNil(t, pools["net/v4"])
		assert.NotNil(t, pools["net/v4"].broadcast)

		// Only .2 remains usable; .3 is broadcast.
		ips, err := allocateFromCIDR("net/v4", family.CIDR, 1, pools, false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.2"}, ips)

		_, err = allocateFromCIDR("net/v4", family.CIDR, 1, pools, false, nil)
		assert.Error(t, err)
	})
}

func TestAllocateDualStack(t *testing.T) {
	newNetworks := func() map[string]*resolver.ResolvedNetwork {
		return map[string]*resolver.ResolvedNetwork{
			"net": {
				Name: "net",
				Spec: nc.NetworkSpec{
					IPv4: &nc.IPNetwork{CIDR: "10.0.0.0/24"},
					IPv6: &nc.IPNetwork{CIDR: "fd00::/128"}, // only one routed address
				},
			},
		}
	}

	a := &Allocator{}

	t.Run("IPv6 exhaustion does not discard IPv4 allocation", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		// Request 2 addresses: IPv4 /24 has room, IPv6 /128 is exhausted past 1.
		alloc, err := a.allocate("net", 2, newNetworks(), pools, true, nil)
		require.Error(t, err)
		require.NotNil(t, alloc)
		assert.Len(t, alloc.IPv4, 2)
		assert.Empty(t, alloc.IPv6)
		assert.Contains(t, err.Error(), "IPv6")
	})

	t.Run("both families succeed when both have room", func(t *testing.T) {
		nets := newNetworks()
		nets["net"].Spec.IPv6 = &nc.IPNetwork{CIDR: "fd00::/120"}
		pools := make(map[string]*networkPool)
		alloc, err := a.allocate("net", 2, nets, pools, true, nil)
		require.NoError(t, err)
		assert.Len(t, alloc.IPv4, 2)
		assert.Len(t, alloc.IPv6, 2)
	})

	t.Run("network not found returns nil allocation", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		alloc, err := a.allocate("missing", 1, newNetworks(), pools, true, nil)
		require.Error(t, err)
		assert.Nil(t, alloc)
	})
}
