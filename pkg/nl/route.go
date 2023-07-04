package nl

import (
	"errors"
	"fmt"

	"github.com/vishvananda/netlink"
)

func getFamily(addressFamily int) (string, error) {

	switch addressFamily {
	case netlink.FAMILY_V4:
		return "ipv4", nil
	case netlink.FAMILY_V6:
		return "ipv6", nil
	case netlink.FAMILY_MPLS:
		return "mpls", nil
	case netlink.FAMILY_ALL:
		return "all", nil
	default:
		return "", errors.New("can't find the addressFamily required")
	}
}

type RouteInformation struct {
	TableId       int
	VrfName       string
	RouteProtocol netlink.RouteProtocol
	AddressFamily string
	Quantity      int
}

type RouteKey struct {
	tableId, routeProtocol, addressFamily int
}

func (n *NetlinkManager) getVRFNameByInterface(tableId int) (string, error) {
	links, err := n.listVRFInterfaces()
	if err != nil {
		return "", err
	}
	for _, link := range links {
		if tableId == link.table {
			return link.Name, nil
		}
	}
	return "", nil
}

func (n *NetlinkManager) getVRFName(tableId int) (string, error) {
	if tableId < 0 || tableId > 255 {
		return "", fmt.Errorf("table id %d out of range [0-255]", tableId)
	}
	switch tableId {
	case 255:
		return "local", nil
	case 254:
		return "main", nil
	case 253:
		return "default", nil
	case 0:
		return "unspecified", nil
	default:
		return n.getVRFNameByInterface(tableId)
	}
}

func (n *NetlinkManager) ListRoutes() ([]RouteInformation, error) {
	netlinkRoutes, err := n.listRoutes()
	routes := map[RouteKey]RouteInformation{}
	if err != nil {
		return nil, err
	}

	for _, netlinkRoute := range netlinkRoutes {
		routeKey := RouteKey{netlinkRoute.Table, int(netlinkRoute.Protocol), netlinkRoute.Family}
		routeInformation, ok := routes[routeKey]
		// If the key exists
		if ok {
			routeInformation.Quantity = routeInformation.Quantity + 1
			routes[routeKey] = routeInformation
		} else {
			family, err := getFamily(netlinkRoute.Family)
			if err != nil {
				return nil, err
			}
			vrfName, err := n.getVRFName(netlinkRoute.Table)
			if err != nil {
				return nil, err
			}
			routes[routeKey] = RouteInformation{
				TableId:       netlinkRoute.Table,
				VrfName:       vrfName,
				RouteProtocol: netlinkRoute.Protocol,
				AddressFamily: family,
				Quantity:      1,
			}
		}
	}
	routeList := []RouteInformation{}
	for _, routeInformation := range routes {
		routeList = append(routeList, routeInformation)
	}
	return routeList, nil
}
