package nl

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func (n *Manager) listRoutes() ([]netlink.Route, error) {
	routes, err := n.toolkit.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{
		Table: 0,
	}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, fmt.Errorf("error listing all routes: %w", err)
	}
	return routes, nil
}

func (n *Manager) listBridgeForwardingTable() ([]netlink.Neigh, error) {
	entries, err := n.toolkit.NeighList(0, unix.AF_BRIDGE)
	if err != nil {
		return nil, fmt.Errorf("error listing bridge fdb entries: %w", err)
	}
	return entries, nil
}

func (n *Manager) listNeighbors() ([]netlink.Neigh, error) {
	neighbors, err := n.toolkit.NeighList(0, netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("error listing all neighbors: %w", err)
	}
	return neighbors, nil
}

func (n *Manager) ListVRFInterfaces() (map[int]VRFInformation, error) {
	// TODO: find a way to merge this with ListL3
	infos := map[int]VRFInformation{}
	links, err := n.toolkit.LinkList()
	if err != nil {
		return nil, fmt.Errorf("cannot get links from netlink: %w", err)
	}

	for _, link := range links {
		if link.Type() != "vrf" {
			continue
		}
		vrf, ok := link.(*netlink.Vrf)
		if !ok {
			return nil, fmt.Errorf("error casting link %v as netlink.Vrf", link)
		}
		info := VRFInformation{}
		info.table = int(vrf.Table)
		info.Name = link.Attrs().Name
		info.vrfID = vrf.Attrs().Index
		infos[info.table] = info
	}

	return infos, nil
}

func (n *Manager) ListNeighborInterfaces() (map[int]netlink.Link, error) {
	links, err := n.toolkit.LinkList()
	neighborLinks := map[int]netlink.Link{}
	if err != nil {
		return nil, fmt.Errorf("error listing links: %w", err)
	}

	for _, link := range links {
		if strings.HasPrefix(link.Attrs().Name, vethL2Prefix) ||
			strings.HasPrefix(link.Attrs().Name, macvlanPrefix) ||
			strings.HasPrefix(link.Attrs().Name, layer2Prefix) ||
			link.Attrs().Vfs != nil {
			neighborLinks[link.Attrs().Index] = link
		}
	}
	return neighborLinks, nil
}

func (n *Manager) ListL3() ([]VRFInformation, error) {
	infos := []VRFInformation{}

	links, err := n.toolkit.LinkList()
	if err != nil {
		return nil, fmt.Errorf("error listing links: %w", err)
	}

	for _, link := range links {
		if !(link.Type() == "vrf" && strings.HasPrefix(link.Attrs().Name, VrfPrefix)) {
			continue
		}
		vrf, ok := link.(*netlink.Vrf)
		if !ok {
			return nil, fmt.Errorf("error casting link %v as netlink.Vrf", link)
		}
		info := VRFInformation{}
		info.table = int(vrf.Table)
		info.Name = link.Attrs().Name[3:]
		info.vrfID = vrf.Attrs().Index

		n.updateL3Indices(&info)

		infos = append(infos, info)
	}

	return infos, nil
}

func (n *Manager) updateL3Indices(info *VRFInformation) {
	bridgeLink, err := n.toolkit.LinkByName(bridgePrefix + info.Name)
	if err == nil {
		info.bridgeID = bridgeLink.Attrs().Index
	} else {
		info.MarkForDelete = true
	}
	vxlanLink, err := n.toolkit.LinkByName(vxlanPrefix + info.Name)
	if err == nil {
		info.VNI = vxlanLink.(*netlink.Vxlan).VxlanId
	} else {
		info.MarkForDelete = true
	}
	vethLink, err := n.toolkit.LinkByName(vrfToDefaultPrefix + info.Name)
	if err == nil {
		info.MTU = vethLink.Attrs().MTU
	} else {
		info.MarkForDelete = true
	}
}

func (n *Manager) updateL2Indices(info *Layer2Information, links []netlink.Link) error {
	for _, link := range links {
		// Check if master of interface is bridge
		if link.Attrs().MasterIndex != info.bridge.Attrs().Index {
			continue
		}

		if err := n.updateLink(info, link); err != nil {
			return err
		}
	}

	// Read IP addresses
	currentV4, err := n.toolkit.AddrList(info.bridge, unix.AF_INET)
	if err != nil {
		return fmt.Errorf("error listing link's IPv4 addresses: %w", err)
	}
	currentV6, err := n.toolkit.AddrList(info.bridge, unix.AF_INET6)
	if err != nil {
		return fmt.Errorf("error listing link's IPv6 addresses: %w", err)
	}
	for i, addr := range currentV4 {
		if addr.Scope != unix.RT_SCOPE_UNIVERSE {
			continue
		}
		info.AnycastGateways = append(info.AnycastGateways, &currentV4[i])
	}
	for i, addr := range currentV6 {
		if addr.Scope != unix.RT_SCOPE_UNIVERSE {
			continue
		}
		info.AnycastGateways = append(info.AnycastGateways, &currentV6[i])
	}
	return nil
}

func (n *Manager) updateLink(info *Layer2Information, link netlink.Link) error {
	// If subinterface is VXLAN
	if link.Type() == "vxlan" && strings.HasPrefix(link.Attrs().Name, vxlanPrefix) {
		vxlan, ok := link.(*netlink.Vxlan)
		if !ok {
			return fmt.Errorf("error casting link %v as netlink.Vxlan", link)
		}
		info.vxlan = vxlan
		info.VNI = info.vxlan.VxlanId
	}

	// If subinterface is VETH
	if link.Type() == "veth" && strings.HasPrefix(link.Attrs().Name, vethL2Prefix) {
		macvlanBridge, ok := link.(*netlink.Veth)
		if !ok {
			return fmt.Errorf("error casting link %v as netlink.Veth", link)
		}
		info.macvlanBridge = macvlanBridge
		peerIdx, err := n.toolkit.VethPeerIndex(info.macvlanBridge)
		if err != nil {
			return fmt.Errorf("error getting veth perr by index: %w", err)
		}
		peerInterface, err := n.toolkit.LinkByIndex(peerIdx)
		if err != nil {
			return fmt.Errorf("error getting link by index: %w", err)
		}
		macvlanHost, ok := peerInterface.(*netlink.Veth)
		if !ok {
			return fmt.Errorf("error casting link %v as netlink.Veth", link)
		}
		info.macvlanHost = macvlanHost
	}

	return nil
}

func (n *Manager) ListL2() ([]Layer2Information, error) {
	infos := []Layer2Information{}

	links, err := n.toolkit.LinkList()
	if err != nil {
		return nil, fmt.Errorf("error listing links: %w", err)
	}

	for _, link := range links {
		if !(link.Type() == "bridge" && strings.HasPrefix(link.Attrs().Name, layer2Prefix)) {
			continue
		}
		info := Layer2Information{}

		bridge, ok := link.(*netlink.Bridge)
		if !ok {
			return nil, fmt.Errorf("cannot cast link %v as netlink.Bridge", link)
		}
		info.bridge = bridge
		info.AnycastMAC = &info.bridge.HardwareAddr
		info.MTU = info.bridge.MTU
		vlanID, err := strconv.Atoi(info.bridge.Name[3:])
		if err != nil {
			return nil, fmt.Errorf("error getting vlanID as integer: %w", err)
		}
		info.VlanID = vlanID

		if info.bridge.MasterIndex > 0 {
			vrf, err := n.toolkit.LinkByIndex(info.bridge.MasterIndex)
			if err != nil {
				return nil, fmt.Errorf("error getting link by index: %w", err)
			}
			if vrf.Type() == "vrf" {
				info.VRF = vrf.Attrs().Name[3:]
			}
		}

		err = n.updateL2Indices(&info, links)
		if err != nil {
			return nil, err
		}

		infos = append(infos, info)
	}

	return infos, nil
}

func (*Manager) ListTaas() ([]TaasInformation, error) {
	infos := []TaasInformation{}

	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("error listing links: %w", err)
	}

	for _, link := range links {
		if !(link.Type() == "vrf" && strings.HasPrefix(link.Attrs().Name, taasVrfPrefix)) {
			continue
		}

		info := TaasInformation{
			Name:  link.Attrs().Name,
			Table: int(link.(*netlink.Vrf).Table),
		}

		infos = append(infos, info)
	}

	return infos, nil
}
