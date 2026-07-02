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
	// TODO(preexisting): find a way to merge this with ListL3
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
		if strings.HasPrefix(link.Attrs().Name, vlanPrefix) ||
			strings.HasPrefix(link.Attrs().Name, layer2SVI) ||
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
		if !(link.Type() == "vrf") {
			continue
		}
		name := link.Attrs().Name
		// We do not want to list the cluster or management VRF
		if n.baseConfig.ClusterVRF.Name == name || n.baseConfig.ManagementVRF.Name == name {
			continue
		}
		vrf, ok := link.(*netlink.Vrf)
		if !ok {
			return nil, fmt.Errorf("error casting link %v as netlink.Vrf", link)
		}
		info := VRFInformation{}
		info.table = int(vrf.Table)
		info.Name = name
		info.vrfID = vrf.Attrs().Index

		n.updateL3Indices(&info, links)

		infos = append(infos, info)
	}

	return infos, nil
}

// updateL3Indices recovers the bridge index and VNI of a VRF by walking the link
// list. The L3VNI bridge (br.*) is enslaved to the VRF device and the L3VNI
// VXLAN (vx.*) is enslaved to that bridge; the VNI is read back from the VXLAN.
// This is master-index based (not name based) because bridge/VXLAN interfaces
// are named by VNI, which is exactly what we are trying to discover.
func (*Manager) updateL3Indices(info *VRFInformation, links []netlink.Link) {
	var bridge netlink.Link
	for _, link := range links {
		if link.Type() == linkTypeBridge &&
			strings.HasPrefix(link.Attrs().Name, bridgePrefix) &&
			link.Attrs().MasterIndex == info.vrfID {
			bridge = link
			info.bridgeID = link.Attrs().Index
			break
		}
	}
	if bridge == nil {
		info.MarkForDelete = true
		return
	}

	for _, link := range links {
		if link.Type() == linkTypeVXLAN &&
			strings.HasPrefix(link.Attrs().Name, vxlanPrefix) &&
			link.Attrs().MasterIndex == bridge.Attrs().Index {
			if vxlan, ok := link.(*netlink.Vxlan); ok {
				info.VNI = vxlan.VxlanId
				return
			}
		}
	}
	info.MarkForDelete = true
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
		info.AnycastGateways = append(info.AnycastGateways, currentV4[i].IPNet.String())
	}
	for i, addr := range currentV6 {
		if addr.Scope != unix.RT_SCOPE_UNIVERSE {
			continue
		}
		info.AnycastGateways = append(info.AnycastGateways, currentV6[i].IPNet.String())
	}
	return nil
}

func (*Manager) updateLink(info *Layer2Information, link netlink.Link) error {
	// If subinterface is VXLAN
	if link.Type() == linkTypeVXLAN && strings.HasPrefix(link.Attrs().Name, vxlanPrefix) {
		vxlan, ok := link.(*netlink.Vxlan)
		if !ok {
			return fmt.Errorf("error casting link %v as netlink.Vxlan", link)
		}
		info.vxlan = vxlan
		info.VNI = info.vxlan.VxlanId
	}

	// If subinterface is VLAN
	if link.Type() == "vlan" && strings.HasPrefix(link.Attrs().Name, vlanPrefix) {
		vlanInterface, ok := link.(*netlink.Vlan)
		if !ok {
			return fmt.Errorf("error casting link %v as netlink.Veth", link)
		}
		info.vlanInterface = vlanInterface
		if disabled, err := getSegmentationDisabled(vlanInterface); err == nil {
			info.DisableSegmentation = disabled
		}
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
		if !(link.Type() == linkTypeBridge && strings.HasPrefix(link.Attrs().Name, layer2SVI)) {
			continue
		}
		info := Layer2Information{}

		bridge, ok := link.(*netlink.Bridge)
		if !ok {
			return nil, fmt.Errorf("cannot cast link %v as netlink.Bridge", link)
		}
		info.bridge = bridge
		mac := info.bridge.HardwareAddr.String()
		info.AnycastMAC = &mac
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
				info.VRF = vrf.Attrs().Name
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
