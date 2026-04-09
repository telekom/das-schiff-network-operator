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
		ips, err := allocateFromCIDR("test/v4", "10.0.0.0/24", 3, pools)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, ips)
	})

	t.Run("continues from previous allocation", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		ips1, err := allocateFromCIDR("test/v4", "10.0.0.0/24", 2, pools)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, ips1)

		ips2, err := allocateFromCIDR("test/v4", "10.0.0.0/24", 2, pools)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.3", "10.0.0.4"}, ips2)
	})

	t.Run("skips broadcast address", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		// /30 has 4 addresses: .0 (network), .1, .2, .3 (broadcast)
		// Usable: .1, .2 only
		ips, err := allocateFromCIDR("test/v4", "10.0.0.0/30", 2, pools)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, ips)

		// Third allocation should fail — .3 is broadcast
		_, err = allocateFromCIDR("test/v4", "10.0.0.0/30", 1, pools)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "broadcast")
	})

	t.Run("exhaustion", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		// /30 has 2 usable hosts
		_, err := allocateFromCIDR("test/v4", "10.0.0.0/30", 3, pools)
		assert.Error(t, err)
	})

	t.Run("IPv6 allocation", func(t *testing.T) {
		pools := make(map[string]*networkPool)
		ips, err := allocateFromCIDR("test/v6", "fd00::/120", 2, pools)
		require.NoError(t, err)
		assert.Len(t, ips, 2)
		assert.Equal(t, "fd00::1", ips[0])
		assert.Equal(t, "fd00::2", ips[1])
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
		seedPoolFromAddresses("net1/v4", nil, []string{"10.0.0.1", "10.0.0.3"}, pools)
		// nil ipFamily means no-op
		assert.Equal(t, "10.0.0.1", pools["net1/v4"].nextIP.String())
	})
}
