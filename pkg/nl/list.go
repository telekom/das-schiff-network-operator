package nl

import (
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func (n *NetlinkManager) listL3() ([]VRFInformation, error) {
	infos := []VRFInformation{}

	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}

	for _, link := range links {
		if link.Type() == "vrf" && strings.HasPrefix(link.Attrs().Name, VRF_PREFIX) {
			vrf := link.(*netlink.Vrf)

			info := VRFInformation{}
			info.table = int(vrf.Table)
			info.Name = link.Attrs().Name[3:]
			info.vrfId = vrf.Attrs().Index

			err := n.updateL3Indices(&info)
			if err != nil {
				return nil, err
			}

			infos = append(infos, info)
		}
	}

	return infos, nil
}

func (n *NetlinkManager) updateL3Indices(info *VRFInformation) error {
	bridgeLink, err := netlink.LinkByName(BRIDGE_PREFIX + info.Name)
	if err == nil {
		info.bridgeId = bridgeLink.Attrs().Index
	} else {
		info.MarkForDelete = true
	}

	vxlanLink, err := netlink.LinkByName(VXLAN_PREFIX + info.Name)
	if err == nil {
		info.VNI = vxlanLink.(*netlink.Vxlan).VxlanId
	} else {
		info.MarkForDelete = true
	}
	return nil
}

func (n *NetlinkManager) updateL2Indices(info *Layer2Information, links []netlink.Link) error {
	for _, link := range links {
		// Check if master of interface is bridge
		if link.Attrs().MasterIndex != info.bridge.Attrs().Index {
			continue
		}

		// If subinterface is VXLAN
		if link.Type() == "vxlan" && strings.HasPrefix(link.Attrs().Name, VXLAN_PREFIX) {
			info.vxlan = link.(*netlink.Vxlan)
			info.VNI = info.vxlan.VxlanId
		}

		// If subinterface is VETH
		if link.Type() == "veth" && strings.HasPrefix(link.Attrs().Name, VETH_L2_PREFIX) {
			info.macvlanBridge = link.(*netlink.Veth)
			peerIdx, err := netlink.VethPeerIndex(info.macvlanBridge)
			if err != nil {
				return err
			}
			peerInterface, err := netlink.LinkByIndex(peerIdx)
			if err != nil {
				return err
			}
			info.macvlanHost = peerInterface.(*netlink.Veth)
			info.CreateMACVLANInterface = true
		}
	}

	// Read IP addresses
	currentV4, err := netlink.AddrList(info.bridge, unix.AF_INET)
	if err != nil {
		return err
	}
	currentV6, err := netlink.AddrList(info.bridge, unix.AF_INET6)
	if err != nil {
		return err
	}
	for _, addr := range currentV4 {
		if addr.Scope != unix.RT_SCOPE_UNIVERSE {
			continue
		}
		info.AnycastGateways = append(info.AnycastGateways, &addr)
	}
	for _, addr := range currentV6 {
		if addr.Scope != unix.RT_SCOPE_UNIVERSE {
			continue
		}
		info.AnycastGateways = append(info.AnycastGateways, &addr)
	}
	return nil
}

func (n *NetlinkManager) listL2() ([]Layer2Information, error) {
	infos := []Layer2Information{}

	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}

	for _, link := range links {
		if link.Type() == "bridge" && strings.HasPrefix(link.Attrs().Name, LAYER2_PREFIX) {
			info := Layer2Information{}
			info.bridge = link.(*netlink.Bridge)
			info.AnycastMAC = &info.bridge.HardwareAddr
			info.MTU = info.bridge.MTU
			vlanId, err := strconv.Atoi(info.bridge.Name[3:])
			if err != nil {
				return nil, err
			}
			info.VlanID = vlanId

			if info.bridge.MasterIndex > 0 {
				vrf, err := netlink.LinkByIndex(info.bridge.MasterIndex)
				if err != nil {
					return nil, err
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
	}

	return infos, nil
}
