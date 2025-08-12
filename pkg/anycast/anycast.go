package anycast

import (
	"bytes"
	"fmt"
	"net"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/pkg/nltoolkit"
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
	toolkit        nltoolkit.ToolkitInterface
}

func NewTracker(toolkit nltoolkit.ToolkitInterface) *Tracker {
	return &Tracker{TrackedBridges: []int{},
		toolkit: toolkit}
}

func (t *Tracker) checkTrackedInterfaces() {
	logger := ctrl.Log.WithName("anycast")
	for _, intfIdx := range t.TrackedBridges {
		intf, err := t.toolkit.LinkByIndex(intfIdx)
		if err != nil {
			logger.Error(err, "couldn't load interface", "index", intfIdx)
			continue
		}

		_ = syncInterface(intf.(*netlink.Bridge), t.toolkit, logger)
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
		if neighIn[i].State&netlink.NUD_INCOMPLETE == netlink.NUD_INCOMPLETE ||
			neighIn[i].State&netlink.NUD_FAILED == netlink.NUD_FAILED {
			continue
		}
		neighOut = append(neighOut, neighIn[i])
	}
	return neighOut
}

func syncInterfaceByFamily(intf *netlink.Bridge, family int, routingTable uint32, toolkit nltoolkit.ToolkitInterface, logger logr.Logger) error {
	bridgeNeighbors, err := toolkit.NeighList(intf.Attrs().Index, family)
	if err != nil {
		logger.Error(err, "error getting v4 neighbors of interface", "interface", intf.Attrs().Name)
		return fmt.Errorf("error getting v4 neighbors of interface %s: %w", intf.Attrs().Name, err)
	}
	bridgeNeighbors = filterNeighbors(bridgeNeighbors)

	routeFilterV4 := &netlink.Route{
		LinkIndex: intf.Attrs().Index,
		Table:     int(routingTable),
		Protocol:  anycastRoutesProt,
	}
	routes, err := toolkit.RouteListFiltered(family, routeFilterV4, netlink.RT_FILTER_OIF|netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		logger.Error(err, "error getting v4 routes of interface", "interface", intf.Attrs().Name)
		return fmt.Errorf("error getting v4 routes of interface %s: %w", intf.Attrs().Name, err)
	}

	alreadyV4Existing := []*net.IPNet{}
	for i := range routes {
		if !containsIPAddress(bridgeNeighbors, routes[i].Dst) {
			if err := toolkit.RouteDel(&routes[i]); err != nil {
				logger.Error(err, "error deleting route", "route", routes[i])
			}
		} else {
			alreadyV4Existing = append(alreadyV4Existing, routes[i].Dst)
		}
	}

	for i := range bridgeNeighbors {
		ipnet := toolkit.NewIPNet(bridgeNeighbors[i].IP)
		if !containsIPNetwork(alreadyV4Existing, ipnet) {
			route := buildRoute(family, intf, ipnet, routingTable)
			if err := toolkit.RouteAdd(route); err != nil {
				logger.Error(err, "error adding route", "route", routes[i])
			}
		}
	}

	return nil
}

func syncInterface(intf *netlink.Bridge, toolkit nltoolkit.ToolkitInterface, logger logr.Logger) error {
	routingTable := uint32(defaultVrfAnycastTable)
	if intf.Attrs().MasterIndex > 0 {
		nlLink, err := toolkit.LinkByIndex(intf.Attrs().MasterIndex)
		if err != nil {
			logger.Error(err, "error getting VRF parent of interface", "interface", intf.Attrs().Name)
			return fmt.Errorf("error getting VRF parent of interface %s: %w", intf.Attrs().Name, err)
		}
		if nlLink.Type() != "vrf" {
			logger.Info("parent of the interface is not a VRF", "interface", intf.Attrs().Name)
			return fmt.Errorf("parent interface of %s is not a VRF", intf.Attrs().Name)
		}
		routingTable = nlLink.(*netlink.Vrf).Table
	}

	_ = syncInterfaceByFamily(intf, unix.AF_INET, routingTable, toolkit, logger)
	_ = syncInterfaceByFamily(intf, unix.AF_INET6, routingTable, toolkit, logger)

	return nil
}

func (t *Tracker) RunAnycastSync() {
	go func() {
		for {
			t.checkTrackedInterfaces()
			time.Sleep(time.Second)
		}
	}()
}
