package nl

import (
	"fmt"
	"net"

	"github.com/telekom/das-schiff-network-operator/pkg/bpf"
	"github.com/vishvananda/netlink"
)

func (n *NetlinkManager) createVRF(info *VRFInformation) error {
	netlinkVrf := netlink.Vrf{
		LinkAttrs: netlink.LinkAttrs{
			Name: VRF_PREFIX + info.Name,
		},
		Table: uint32(info.table),
	}

	if err := netlink.LinkAdd(&netlinkVrf); err != nil {
		return err
	}
	info.vrfId = netlinkVrf.Attrs().Index

	return netlink.LinkSetUp(&netlinkVrf)
}

func (n *NetlinkManager) createBridge(info *VRFInformation) error {
	netlinkBridge := netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name:        BRIDGE_PREFIX + info.Name,
			MasterIndex: info.vrfId,
			MTU:         DEFAULT_MTU,
		},
	}

	if err := netlink.LinkAdd(&netlinkBridge); err != nil {
		return err
	}
	info.bridgeId = netlinkBridge.Attrs().Index

	if err := netlink.LinkSetUp(&netlinkBridge); err != nil {
		return err
	}

	return bpf.AttachToInterface(&netlinkBridge)
}

func (n *NetlinkManager) createVXLAN(info *VRFInformation) error {
	vxlanIf, vxlanIP, err := getInterfaceAndIP(UNDERLAY_LOOPBACK)
	if err != nil {
		return err
	}

	macAddress, err := generateMAC(vxlanIP)
	if err != nil {
		return err
	}

	netlinkVXLAN := netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:         VXLAN_PREFIX + info.Name,
			MasterIndex:  info.bridgeId,
			MTU:          DEFAULT_MTU,
			HardwareAddr: macAddress,
		},
		VxlanId:      info.VNI,
		VtepDevIndex: vxlanIf,
		SrcAddr:      vxlanIP,
		Learning:     false,
		Port:         4789,
	}
	if err := netlink.LinkAdd(&netlinkVXLAN); err != nil {
		return err
	}
	if err := netlink.LinkSetLearning(&netlinkVXLAN, false); err != nil {
		return err
	}
	if err := netlink.LinkSetHairpin(&netlinkVXLAN, true); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(&netlinkVXLAN); err != nil {
		return err
	}

	return bpf.AttachToInterface(&netlinkVXLAN)
}

func (n *NetlinkManager) createLink(info *VRFInformation) error {
	netlinkVeth := netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name:        VRF_TO_DEFAULT_PREFIX + info.Name,
			MasterIndex: info.vrfId,
			MTU:         DEFAULT_MTU,
		},
		PeerName: DEFAULT_TO_VRF_PREFIX + info.Name,
	}
	if err := netlink.LinkAdd(&netlinkVeth); err != nil {
		return err
	}

	// Enable VRF side of the interface
	if err := netlink.LinkSetUp(&netlinkVeth); err != nil {
		return err
	}
	if err := bpf.AttachToInterface(&netlinkVeth); err != nil {
		return err
	}

	// Search for other side and enable
	if err := netlink.LinkSetUp(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: DEFAULT_TO_VRF_PREFIX + info.Name}}); err != nil {
		return err
	}

	return nil
}

func getInterfaceAndIP(name string) (int, net.IP, error) {
	dummy := netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}

	addresses, err := netlink.AddrList(&dummy, netlink.FAMILY_V4)
	if err != nil {
		return -1, nil, err
	}
	if len(addresses) != 1 {
		return -1, nil, fmt.Errorf("count of v4 addresses on %s do not exactly match 1", dummy.Attrs().Name)
	}

	return dummy.Attrs().Index, addresses[0].IP, nil
}

func generateMAC(ip net.IP) (net.HardwareAddr, error) {
	if ip.To4() == nil {
		return nil, fmt.Errorf("generateMAC is only working with IPv4 addresses")
	}
	hwaddr := make([]byte, 6)
	copy(hwaddr, MAC_PREFIX)
	copy(hwaddr[2:], ip.To4())
	return hwaddr, nil
}
