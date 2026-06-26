package agent_hbn_l2 //nolint:revive

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"

	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
)

func TestParseRoutes(t *testing.T) {
	t.Run("no routes", func(t *testing.T) {
		dev := netplan.Device{Raw: []byte(`id: 501
mtu: 9000
`)}
		routes, err := parseRoutes(dev)
		require.NoError(t, err)
		assert.Empty(t, routes)
	})

	t.Run("single default route", func(t *testing.T) {
		dev := netplan.Device{Raw: []byte(`id: 501
routes:
  - to: default
    via: 10.0.1.1
`)}
		routes, err := parseRoutes(dev)
		require.NoError(t, err)
		require.Len(t, routes, 1)
		assert.Equal(t, "default", routes[0].To)
		assert.Equal(t, "10.0.1.1", routes[0].Via)
	})

	t.Run("multiple routes", func(t *testing.T) {
		dev := netplan.Device{Raw: []byte(`routes:
  - to: default
    via: 10.0.1.1
  - to: default
    via: "2001:db8::1"
`)}
		routes, err := parseRoutes(dev)
		require.NoError(t, err)
		require.Len(t, routes, 2)
	})
}

func TestToNetlinkRoute(t *testing.T) {
	t.Run("IPv4 default route", func(t *testing.T) {
		r, err := toNetlinkRoute(5, routeConfig{To: "default", Via: "10.0.1.1"})
		require.NoError(t, err)
		assert.Equal(t, 5, r.LinkIndex)
		assert.Equal(t, "0.0.0.0/0", r.Dst.String())
		assert.True(t, r.Gw.Equal(net.ParseIP("10.0.1.1")))
		assert.Equal(t, rtprotHBN, r.Protocol)
	})

	t.Run("IPv6 default route", func(t *testing.T) {
		r, err := toNetlinkRoute(7, routeConfig{To: "default", Via: "2001:db8::1"})
		require.NoError(t, err)
		assert.Equal(t, "::/0", r.Dst.String())
		assert.True(t, r.Gw.Equal(net.ParseIP("2001:db8::1")))
	})

	t.Run("specific prefix route", func(t *testing.T) {
		r, err := toNetlinkRoute(3, routeConfig{To: "192.168.0.0/16", Via: "10.0.1.1"})
		require.NoError(t, err)
		assert.Equal(t, "192.168.0.0/16", r.Dst.String())
	})

	t.Run("invalid gateway", func(t *testing.T) {
		_, err := toNetlinkRoute(1, routeConfig{To: "default", Via: "not-an-ip"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid gateway")
	})

	t.Run("invalid destination", func(t *testing.T) {
		_, err := toNetlinkRoute(1, routeConfig{To: "not-a-cidr", Via: "10.0.1.1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid destination")
	})
}

func TestIsDesiredRoute(t *testing.T) {
	_, dst1, _ := net.ParseCIDR("0.0.0.0/0")
	_, dst2, _ := net.ParseCIDR("192.168.0.0/16")

	desired := []netlink.Route{
		{LinkIndex: 5, Dst: dst1, Gw: net.ParseIP("10.0.1.1")},
		{LinkIndex: 5, Dst: dst2, Gw: net.ParseIP("10.0.1.1")},
	}

	t.Run("match found", func(t *testing.T) {
		candidate := &netlink.Route{LinkIndex: 5, Dst: dst1, Gw: net.ParseIP("10.0.1.1")}
		assert.True(t, isDesiredRoute(candidate, desired))
	})

	t.Run("different gateway", func(t *testing.T) {
		candidate := &netlink.Route{LinkIndex: 5, Dst: dst1, Gw: net.ParseIP("10.0.1.99")}
		assert.False(t, isDesiredRoute(candidate, desired))
	})

	t.Run("different link", func(t *testing.T) {
		candidate := &netlink.Route{LinkIndex: 99, Dst: dst1, Gw: net.ParseIP("10.0.1.1")}
		assert.False(t, isDesiredRoute(candidate, desired))
	})

	t.Run("empty desired list", func(t *testing.T) {
		candidate := &netlink.Route{LinkIndex: 5, Dst: dst1, Gw: net.ParseIP("10.0.1.1")}
		assert.False(t, isDesiredRoute(candidate, nil))
	})
}
