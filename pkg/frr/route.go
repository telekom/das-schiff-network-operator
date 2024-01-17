package frr

import (
	"fmt"
	"strconv"

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/route"
	"github.com/vishvananda/netlink"
)

func getQuantity(routeSummaries RouteSummaries, addressFamily int, vrf, table string) ([]route.Information, error) {
	// _ is the cidr and is ignored.
	routeSummaryList := []route.Information{}
	routeSummaryMap := map[route.Key]route.Information{}
	for _, routeSummary := range routeSummaries.Routes {
		routeProtocol := netlink.RouteProtocol(nl.GetProtocolNumber(routeSummary.Type, true))
		family, err := nl.GetAddressFamily(addressFamily)
		if err != nil {
			return nil, fmt.Errorf("error converting addressFamily [%d]: %w", addressFamily, err)
		}
		tableID, err := strconv.Atoi(table)
		if err != nil {
			return nil, fmt.Errorf("error converting string to integer [%s]: %w", table, err)
		}
		routeKey := route.Key{TableID: tableID, RouteProtocol: int(routeProtocol), AddressFamily: addressFamily}
		routeInformation, ok := routeSummaryMap[routeKey]
		if !ok {
			routeSummaryMap[routeKey] = route.Information{
				TableID:       tableID,
				VrfName:       vrf,
				RouteProtocol: routeProtocol,
				AddressFamily: family,
				Fib:           routeSummary.Fib,
				Rib:           routeSummary.Rib,
			}
		} else {
			// if we have ibgp and ebgp they both.
			routeInformation.Rib = routeInformation.Rib + routeSummary.Rib
			routeInformation.Fib = routeInformation.Fib + routeSummary.Fib
			routeSummaryMap[routeKey] = routeInformation
		}
	}
	for _, routeSummary := range routeSummaryMap {
		routeSummaryList = append(routeSummaryList, routeSummary)
	}
	return routeSummaryList, nil
}

func (m *Manager) ListVrfs() ([]VrfVniSpec, error) {
	vrfs, err := m.Cli.ShowVRFs("")
	if err != nil {
		return vrfs.Vrfs, fmt.Errorf("cannot get all vrfs: %w", err)
	}
	vrfs.Vrfs = Filter(vrfs.Vrfs, func(vrf VrfVniSpec) bool {
		return vrf.State != ""
	})
	return vrfs.Vrfs, nil
}

func (m *Manager) ListRouteSummary(vrf string) ([]route.Information, error) {
	vrfDualStackRouteSummaries, err := m.Cli.ShowRouteSummary(vrf)
	if err != nil {
		return nil, fmt.Errorf("cannot get Routes for vrf %s: %w", vrf, err)
	}

	routeList := []route.Information{}
	for vrfName, dualStackRouteSummary := range vrfDualStackRouteSummaries {
		routeInfoV4, err := getQuantity(dualStackRouteSummary.IPv4, netlink.FAMILY_V4, vrfName, dualStackRouteSummary.Table)
		if err != nil {
			return nil, fmt.Errorf("cannot calculate number of ipv4 routes in vrf %s: %w", vrf, err)
		}
		routeInfoV6, err := getQuantity(dualStackRouteSummary.IPv6, netlink.FAMILY_V6, vrfName, dualStackRouteSummary.Table)
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
