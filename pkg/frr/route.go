package frr

import (
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/route"
	"github.com/vishvananda/netlink"
)

func getQuantity(routeInfos Routes, family int) ([]route.RouteInformation, error) {
	routes := map[route.RouteKey]route.RouteInformation{}
	// _ is the cidr and is ignored.
	for _, paths := range routeInfos {
		for _, routePath := range paths {
			routeProtocol := netlink.RouteProtocol(nl.GetProtocolNumber(routePath.Protocol, true))
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

func (frr *FRRManager) ListVrfs() ([]VrfVniSpec, error) {
	vrfs, err := frr.CLI.ShowVRFs()
	if err != nil {
		return vrfs.Vrfs, err
	}
	vrfs.Vrfs = Filter(vrfs.Vrfs, func(vrf VrfVniSpec) bool {
		return vrf.State != ""
	})
	return vrfs.Vrfs, nil
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

func (frr *FRRManager) ListNeighbors(vrf string) (bgpSummary BGPVrfSummary, err error) {
	bgpSummary, err = frr.CLI.ShowBGPSummary(vrf)
	if err != nil {
		return bgpSummary, err
	}
	return bgpSummary, nil
}
