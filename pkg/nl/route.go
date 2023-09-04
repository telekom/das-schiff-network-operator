package nl

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/telekom/das-schiff-network-operator/pkg/route"
	schiff_unix "github.com/telekom/das-schiff-network-operator/pkg/unix"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func GetProtocolName(p netlink.RouteProtocol) string {
	protocol := p.String()
	integer, err := strconv.Atoi(protocol)
	if err != nil {
		return protocol
	}
	switch integer {
	case schiff_unix.RTPROT_NHRP:
		return "nhrp"
	case schiff_unix.RTPROT_LDP:
		return "ldp"
	case schiff_unix.RTPROT_SHARP:
		return "sharp"
	case schiff_unix.RTPROT_PBR:
		return "pbr"
	case schiff_unix.RTPROT_ZSTATIC:
		return "zstatic"
	case schiff_unix.RTPROT_OPENFABRIC:
		return "openfabric"
	case schiff_unix.RTPROT_SRTE:
		return "srte"
	case schiff_unix.RTPROT_COIL:
		return "coil"
	default:
		return strconv.Itoa(int(p))
	}
}

func GetProtocolNumber(protocol string, frr bool) int {
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
		if frr {
			// frr has its own static protocol
			return schiff_unix.RTPROT_ZSTATIC
		}
		return unix.RTPROT_STATIC
	case "unspec":
		return unix.RTPROT_UNSPEC
	case "xorp":
		return unix.RTPROT_XORP
	case "zebra":
		return unix.RTPROT_ZEBRA
	// this is a hack as there is no direct representation in Linux for
	// directly connected routes but normally they are installed by kernel
	case "connected":
		return unix.RTPROT_KERNEL
	//
	case "nhrp":
		return schiff_unix.RTPROT_NHRP
	case "ldp":
		return schiff_unix.RTPROT_LDP
	case "sharp":
		return schiff_unix.RTPROT_SHARP
	case "pbr":
		return schiff_unix.RTPROT_PBR
	case "zstatic":
		return schiff_unix.RTPROT_ZSTATIC
	case "openfabric":
		return schiff_unix.RTPROT_OPENFABRIC
	case "srte":
		return schiff_unix.RTPROT_SRTE
	case "coil":
		return schiff_unix.RTPROT_COIL
	default:
		panic(fmt.Sprintf("The protocol %s cannot be converted to unix Enum", protocol))
	}
}

func GetAddressFamily(addressFamily int) (string, error) {
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

func (n *NetlinkManager) getVRFNameByInterface(tableId int) (string, error) {
	links, err := n.listVRFInterfaces()
	if err != nil {
		return "", fmt.Errorf("error getting vrf interfaces: %w", err)
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
	case localTableID:
		return "local", nil
	case mainTableID:
		return "main", nil
	case defaultTableID:
		return "default", nil
	case unspecifiedTableID:
		return "unspecified", nil
	default:
		return n.getVRFNameByInterface(tableId)
	}
}

func (n *NetlinkManager) ListRoutes() ([]route.Information, error) {
	netlinkRoutes, err := n.listRoutes()
	if err != nil {
		return nil, fmt.Errorf("error listing routes: %w", err)
	}
	routes := map[route.Key]route.Information{}

	for _, netlinkRoute := range netlinkRoutes {
		routeKey := route.Key{TableID: netlinkRoute.Table, RouteProtocol: int(netlinkRoute.Protocol), AddressFamily: netlinkRoute.Family}
		routeInformation, ok := routes[routeKey]
		// If the key exists
		if ok {
			routeInformation.Quantity++
			routes[routeKey] = routeInformation
		} else {
			family, err := GetAddressFamily(netlinkRoute.Family)
			if err != nil {
				return nil, fmt.Errorf("error converting addressFamily [%d]: %w", netlinkRoute.Family, err)
			}
			vrfName, err := n.getVRFName(netlinkRoute.Table)
			if err != nil {
				return nil, fmt.Errorf("error getting vrfName for table id %d: %w", netlinkRoute.Table, err)
			}
			routes[routeKey] = route.Information{
				TableID:       netlinkRoute.Table,
				VrfName:       vrfName,
				RouteProtocol: netlinkRoute.Protocol,
				AddressFamily: family,
				Quantity:      1,
			}
		}
	}
	routeList := []route.Information{}
	for _, routeInformation := range routes {
		routeList = append(routeList, routeInformation)
	}
	return routeList, nil
}
