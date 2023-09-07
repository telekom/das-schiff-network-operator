package frr

import (
	"fmt"

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/route"
	"github.com/vishvananda/netlink"
)

func getQuantity(routeInfos Routes, addressFamily int) ([]route.Information, error) {
	routes := map[route.Key]route.Information{}
	// _ is the cidr and is ignored.
	for index := range routeInfos {
		for innerIndex := range routeInfos[index] {
			routeProtocol := netlink.RouteProtocol(nl.GetProtocolNumber(routeInfos[index][innerIndex].Protocol, true))
			routeKey := route.Key{TableID: routeInfos[index][innerIndex].Table, RouteProtocol: int(routeProtocol), AddressFamily: addressFamily}

			routeInformation, ok := routes[routeKey]
			if ok {
				// count one up
				routeInformation.Quantity++
				routes[routeKey] = routeInformation
			} else {
				family, err := nl.GetAddressFamily(addressFamily)
				if err != nil {
					return nil, fmt.Errorf("error converting addressFamily [%d]: %w", addressFamily, err)
				}
				routes[routeKey] = route.Information{
					TableID:       routeInfos[index][innerIndex].Table,
					VrfName:       routeInfos[index][innerIndex].VrfName,
					RouteProtocol: routeProtocol,
					AddressFamily: family,
					Quantity:      1,
				}
			}
		}
	}
	routeList := []route.Information{}
	for _, routeInformation := range routes {
		routeList = append(routeList, routeInformation)
	}
	return routeList, nil
}

func (m *Manager) ListVrfs() ([]VrfVniSpec, error) {
	vrfs, err := m.Cli.ShowVRFs()
	if err != nil {
		return vrfs.Vrfs, fmt.Errorf("cannot get all vrfs: %w", err)
	}
	vrfs.Vrfs = Filter(vrfs.Vrfs, func(vrf VrfVniSpec) bool {
		return vrf.State != ""
	})
	return vrfs.Vrfs, nil
}

func (m *Manager) ListRoutes(vrf string) ([]route.Information, error) {
	vrfDualStackRoutes, err := m.Cli.ShowRoutes(vrf)
	if err != nil {
		return nil, fmt.Errorf("cannot get Routes for vrf %s: %w", vrf, err)
	}

	routeList := []route.Information{}
	for _, dualStackRoutes := range vrfDualStackRoutes {
		routeInfoV4, err := getQuantity(dualStackRoutes.IPv4, netlink.FAMILY_V4)
		if err != nil {
			return nil, fmt.Errorf("cannot calculate number of ipv4 routes in vrf %s: %w", vrf, err)
		}
		routeInfoV6, err := getQuantity(dualStackRoutes.IPv6, netlink.FAMILY_V6)
		if err != nil {
			return nil, fmt.Errorf("cannot calculate number of ipv6 routes in vrf %s: %w", vrf, err)
		}
		routeList = append(routeList, routeInfoV4...)
		routeList = append(routeList, routeInfoV6...)
	}

	return routeList, nil
}

func (m *Manager) ListNeighbors(vrf string) (bgpSummary BGPVrfSummary, err error) {
	bgpSummary, err = m.Cli.ShowBGPSummary(vrf)
	if err != nil {
		return bgpSummary, fmt.Errorf("cannot get BGPSummary for vrf %s: %w", vrf, err)
	}
	return bgpSummary, nil
}
