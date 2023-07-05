package frr

import (
	"fmt"

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/route"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func getRoutingProtocol(protocol string) int {
	switch protocol {
	case "babel":
		return unix.RTPROT_BABEL
	case "bgp":
		return unix.RTPROT_BGP
	case "bird":
		return unix.RTPROT_BIRD
	case "boot":
		return unix.RTPROT_BOOT
	case "dhcp":
		return unix.RTPROT_DHCP
	case "dnrouted":
		return unix.RTPROT_DNROUTED
	case "eigrp":
		return unix.RTPROT_EIGRP
	case "gated":
		return unix.RTPROT_GATED
	case "isis":
		return unix.RTPROT_ISIS
	case "kernel":
		return unix.RTPROT_KERNEL
	case "mrouted":
		return unix.RTPROT_MROUTED
	case "mrt":
		return unix.RTPROT_MRT
	case "ntk":
		return unix.RTPROT_NTK
	case "ospf":
		return unix.RTPROT_OSPF
	case "ra":
		return unix.RTPROT_RA
	case "redirect":
		return unix.RTPROT_REDIRECT
	case "rip":
		return unix.RTPROT_RIP
	case "static":
		return unix.RTPROT_STATIC
	case "unspec":
		return unix.RTPROT_UNSPEC
	case "xorp":
		return unix.RTPROT_XORP
	case "zebra":
		return unix.RTPROT_ZEBRA
	default:
		panic(fmt.Sprintf("The protocol %s cannot be converted to unix Enum", protocol))
	}
}

func getQuantity(routeInfos Routes, family int) ([]route.RouteInformation, error) {
	routes := map[route.RouteKey]route.RouteInformation{}
	// _ is the cidr and is ignored.
	for _, paths := range routeInfos {
		for _, routePath := range paths {
			routeProtocol := netlink.RouteProtocol(getRoutingProtocol(routePath.Protocol))
			routeKey := route.RouteKey{TableId: routePath.Table, RouteProtocol: int(routeProtocol), AddressFamily: family}

			routeInformation, ok := routes[routeKey]
			if ok {
				routeInformation.Quantity = routeInformation.Quantity + 1
				routes[routeKey] = routeInformation
			} else {
				family, err := nl.GetFamily(family)
				if err != nil {
					return nil, err
				}
				routes[routeKey] = route.RouteInformation{
					TableId:       routePath.Table,
					VrfName:       routePath.VrfName,
					RouteProtocol: routeProtocol,
					AddressFamily: family,
					Quantity:      1,
				}
			}
		}
	}
	routeList := []route.RouteInformation{}
	for _, routeInformation := range routes {
		routeList = append(routeList, routeInformation)
	}
	return routeList, nil
}

func (frr *FRRManager) ListRoutes(vrf string) ([]route.RouteInformation, error) {
	vrfDualStackRoutes, err := frr.CLI.ShowRoutes(vrf)
	if err != nil {
		return nil, err
	}

	routeList := []route.RouteInformation{}
	for _, dualStackRoutes := range vrfDualStackRoutes {
		routeInfoV4, err := getQuantity(dualStackRoutes.IPv4, netlink.FAMILY_V4)
		if err != nil {
			return nil, err
		}
		routeInfoV6, err := getQuantity(dualStackRoutes.IPv6, netlink.FAMILY_V6)
		if err != nil {
			return nil, err
		}
		routeList = append(routeList, routeInfoV4...)
		routeList = append(routeList, routeInfoV6...)
	}

	return routeList, nil
}
