package nl

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func (n *NetlinkManager) ListL3() ([]VRFInformation, error) {
	infos := []VRFInformation{}

	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("error listing links: %w", err)
	}

	for _, link := range links {
		if !(link.Type() == "vrf" && strings.HasPrefix(link.Attrs().Name, vrfPrefix)) {
			continue
		}
		vrf := link.(*netlink.Vrf)

		info := VRFInformation{}
		info.table = int(vrf.Table)
		info.Name = link.Attrs().Name[3:]
		info.vrfID = vrf.Attrs().Index

		err := n.updateL3Indices(&info)
		if err != nil {
			return nil, err
		}

		infos = append(infos, info)
	}

	return infos, nil
}

func (*NetlinkManager) updateL3Indices(info *VRFInformation) error {
	bridgeLink, err := netlink.LinkByName(bridgePrefix + info.Name)
	if err != nil {
		return fmt.Errorf("error getting link by name: %w", err)
	}
	vxlanLink, err := netlink.LinkByName(vxlanPrefix + info.Name)
	if err != nil {
		return fmt.Errorf("error getting link by name: %w", err)
	}
	netlinkBridge := bridgeLink.(*netlink.Bridge)
	netlinkVXLAN := vxlanLink.(*netlink.Vxlan)

	info.bridgeID = netlinkBridge.Attrs().Index
	info.VNI = netlinkVXLAN.VxlanId
	return nil
}

func (*NetlinkManager) updateL2Indices(info *Layer2Information, links []netlink.Link) error {
	for _, link := range links {
		// Check if master of interface is bridge
		if link.Attrs().MasterIndex != info.bridge.Attrs().Index {
			continue
		}

		if err := updateLink(info, link); err != nil {
			return err
		}
	}

	// Read IP addresses
	currentV4, err := netlink.AddrList(info.bridge, unix.AF_INET)
	if err != nil {
		return fmt.Errorf("error listing link's IPv4 addresses: %w", err)
	}
	currentV6, err := netlink.AddrList(info.bridge, unix.AF_INET6)
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

func updateLink(info *Layer2Information, link netlink.Link) error {
	// If subinterface is VXLAN
	if link.Type() == "vxlan" && strings.HasPrefix(link.Attrs().Name, vxlanPrefix) {
		info.vxlan = link.(*netlink.Vxlan)
		info.VNI = info.vxlan.VxlanId
	}

	// If subinterface is VETH
	if link.Type() == "veth" && strings.HasPrefix(link.Attrs().Name, vethL2Prefix) {
		info.macvlanBridge = link.(*netlink.Veth)
		peerIdx, err := netlink.VethPeerIndex(info.macvlanBridge)
		if err != nil {
			return fmt.Errorf("error getting veth perr by index: %w", err)
		}
		peerInterface, err := netlink.LinkByIndex(peerIdx)
		if err != nil {
			return fmt.Errorf("error getting link by index: %w", err)
		}
		info.macvlanHost = peerInterface.(*netlink.Veth)
		info.CreateMACVLANInterface = true
	}

	return nil
}

func (n *NetlinkManager) ListL2() ([]Layer2Information, error) {
	infos := []Layer2Information{}

	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("error listing links: %w", err)
	}

	for _, link := range links {
		if !(link.Type() == "bridge" && strings.HasPrefix(link.Attrs().Name, layer2Prefix)) {
			continue
		}
		info := Layer2Information{}
		info.bridge = link.(*netlink.Bridge)
		info.AnycastMAC = &info.bridge.HardwareAddr
		info.MTU = info.bridge.MTU
		vlanID, err := strconv.Atoi(info.bridge.Name[3:])
		if err != nil {
			return nil, fmt.Errorf("error getting vlanID as integer: %w", err)
		}
		info.VlanID = vlanID

		if info.bridge.MasterIndex > 0 {
			vrf, err := netlink.LinkByIndex(info.bridge.MasterIndex)
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
