package anycast

import (
	"bytes"
	"net"
	"time"

	"github.com/go-logr/logr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	routeProtocol          = 125
	anycastRoutesProt      = netlink.RouteProtocol(routeProtocol)
	defaultVrfAnycastTable = 130
)

type Tracker struct {
	TrackedBridges []int
}

// TODO: Anycast Support is currently highly experimental.

func (t *Tracker) checkTrackedInterfaces() {
	logger := ctrl.Log.WithName("anycast")
	for _, intfIdx := range t.TrackedBridges {
		intf, err := netlink.LinkByIndex(intfIdx)
		if err != nil {
			logger.Error(err, "couldn't load interface idx %d", intfIdx)
			continue
		}

		syncInterface(intf.(*netlink.Bridge), logger)
	}
}

func containsIPNetwork(list []*net.IPNet, dst *net.IPNet) bool {
	for _, v := range list {
		if v.IP.Equal(dst.IP) && bytes.Equal(v.Mask, dst.Mask) {
			return true
		}
	}
	return false
}

func containsIPAddress(list []netlink.Neigh, dst *net.IPNet) bool {
	for i := range list {
		if list[i].IP.Equal(dst.IP) {
			return true
		}
	}
	return false
}

func buildRoute(family int, intf *netlink.Bridge, dst *net.IPNet, table uint32) *netlink.Route {
	return &netlink.Route{
		Family:    family,
		Protocol:  anycastRoutesProt,
		LinkIndex: intf.Attrs().Index,
		Dst:       dst,
		Table:     int(table),
	}
}

func filterNeighbors(neighIn []netlink.Neigh) (neighOut []netlink.Neigh) {
	for i := range neighIn {
		if neighIn[i].Flags&netlink.NTF_EXT_LEARNED == netlink.NTF_EXT_LEARNED {
			continue
		}
		if neighIn[i].State != netlink.NUD_NONE &&
			neighIn[i].State&netlink.NUD_PERMANENT != netlink.NUD_PERMANENT &&
			neighIn[i].State&netlink.NUD_STALE != netlink.NUD_STALE &&
			neighIn[i].State&netlink.NUD_REACHABLE != netlink.NUD_REACHABLE &&
			neighIn[i].State&netlink.NUD_DELAY != netlink.NUD_DELAY {
			continue
		}
		neighOut = append(neighOut, neighIn[i])
	}
	return neighOut
}

func syncInterfaceByFamily(intf *netlink.Bridge, family int, routingTable uint32, logger logr.Logger) {
	bridgeNeighbors, err := netlink.NeighList(intf.Attrs().Index, family)
	if err != nil {
		logger.Error(err, "error getting v4 neighbors of interface %s", intf.Attrs().Name)
		return
	}
	bridgeNeighbors = filterNeighbors(bridgeNeighbors)

	routeFilterV4 := &netlink.Route{
		LinkIndex: intf.Attrs().Index,
		Table:     int(routingTable),
		Protocol:  anycastRoutesProt,
	}
	routes, err := netlink.RouteListFiltered(family, routeFilterV4, netlink.RT_FILTER_OIF|netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		logger.Error(err, "error getting v4 routes of interface %s", intf.Attrs().Name)
		return
	}

	alreadyV4Existing := []*net.IPNet{}
	for i := range routes {
		if !containsIPAddress(bridgeNeighbors, routes[i].Dst) {
			if err := netlink.RouteDel(&routes[i]); err != nil {
				logger.Error(err, "error deleting route %v", routes[i])
			}
		} else {
			alreadyV4Existing = append(alreadyV4Existing, routes[i].Dst)
		}
	}

	for i := range bridgeNeighbors {
		ipnet := netlink.NewIPNet(bridgeNeighbors[i].IP)
		if !containsIPNetwork(alreadyV4Existing, ipnet) {
			route := buildRoute(family, intf, ipnet, routingTable)
			if err := netlink.RouteAdd(route); err != nil {
				logger.Error(err, "error adding route %v", route)
			}
		}
	}
}

func syncInterface(intf *netlink.Bridge, logger logr.Logger) {
	routingTable := uint32(defaultVrfAnycastTable)
	if intf.Attrs().MasterIndex > 0 {
		nl, err := netlink.LinkByIndex(intf.Attrs().MasterIndex)
		if err != nil {
			logger.Error(err, "error getting VRF parent of interface %s", intf.Attrs().Name)
			return
		}
		if nl.Type() != "vrf" {
			logger.Info("parent interface of %s is not a VRF: %v\n", intf.Attrs().Name, err)
			return
		}
		routingTable = nl.(*netlink.Vrf).Table
	}

	syncInterfaceByFamily(intf, unix.AF_INET, routingTable, logger)
	syncInterfaceByFamily(intf, unix.AF_INET6, routingTable, logger)
}

func (t *Tracker) RunAnycastSync() {
	go func() {
		for {
			t.checkTrackedInterfaces()
			time.Sleep(time.Second)
		}
	}()
}
