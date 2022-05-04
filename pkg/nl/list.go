package nl

import (
	"strings"

	"github.com/vishvananda/netlink"
)

func (n *NetlinkManager) list() ([]*VRFInformation, error) {
	infos := []*VRFInformation{}

	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}

	for _, link := range links {
		if link.Type() == "vrf" && strings.HasPrefix(link.Attrs().Name, VRF_PREFIX) {
			vrf := link.(*netlink.Vrf)

			info := &VRFInformation{}
			info.table = int(vrf.Table)
			info.Name = link.Attrs().Name[3:]
			info.vrfId = vrf.Attrs().Index

			err := n.updateIndices(info)
			if err != nil {
				return nil, err
			}

			infos = append(infos, info)
		}
	}

	return infos, nil
}

func (n *NetlinkManager) updateIndices(info *VRFInformation) error {
	bridgeLink, err := netlink.LinkByName(BRIDGE_PREFIX + info.Name)
	if err != nil {
		return err
	}
	vxlanLink, err := netlink.LinkByName(VXLAN_PREFIX + info.Name)
	if err != nil {
		return err
	}
	netlinkBridge := bridgeLink.(*netlink.Bridge)
	netlinkVXLAN := vxlanLink.(*netlink.Vxlan)

	info.bridgeId = netlinkBridge.Attrs().Index
	info.VNI = netlinkVXLAN.VxlanId
	return nil
}
