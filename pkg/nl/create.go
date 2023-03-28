package nl

import (
	"fmt"
	"net"
	"os"

	"github.com/vishvananda/netlink"
)

func (n *NetlinkManager) createVRF(vrfName string, table int) (*netlink.Vrf, error) {
	netlinkVrf := netlink.Vrf{
		LinkAttrs: netlink.LinkAttrs{
			Name: vrfName,
		},
		Table: uint32(table),
	}

	if err := netlink.LinkAdd(&netlinkVrf); err != nil {
		return nil, err
	}
	if err := n.disableEUIAutogeneration(vrfName); err != nil {
		return nil, err
	}
	if err := netlink.LinkSetUp(&netlinkVrf); err != nil {
		return nil, err
	}

	return &netlinkVrf, nil
}

func (n *NetlinkManager) createBridge(bridgeName string, masterIdx int, mtu int) (*netlink.Bridge, error) {
	netlinkBridge := netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name: bridgeName,
			MTU:  mtu,
		},
	}
	if masterIdx != -1 {
		netlinkBridge.LinkAttrs.MasterIndex = masterIdx
	}

	if err := netlink.LinkAdd(&netlinkBridge); err != nil {
		return nil, err
	}
	if err := n.disableEUIAutogeneration(bridgeName); err != nil {
		return nil, err
	}

	return &netlinkBridge, nil
}

func (n *NetlinkManager) createVXLAN(vxlanName string, bridgeIdx int, macAddress *net.HardwareAddr, vni int, mtu int, hairpin bool) (*netlink.Vxlan, error) {
	vxlanIf, vxlanIP, err := getInterfaceAndIP(UNDERLAY_LOOPBACK)
	if err != nil {
		return nil, err
	}

	if macAddress == nil {
		generatedMac, err := generateMAC(vxlanIP)
		if err != nil {
			return nil, err
		}
		macAddress = &generatedMac
	}

	netlinkVXLAN := netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:         vxlanName,
			MasterIndex:  bridgeIdx,
			MTU:          mtu,
			HardwareAddr: *macAddress,
		},
		VxlanId:      vni,
		VtepDevIndex: vxlanIf,
		SrcAddr:      vxlanIP,
		Learning:     false,
		Port:         4789,
	}
	if err := netlink.LinkAdd(&netlinkVXLAN); err != nil {
		return nil, err
	}
	if err := netlink.LinkSetLearning(&netlinkVXLAN, false); err != nil {
		return nil, err
	}
	if hairpin {
		if err := netlink.LinkSetHairpin(&netlinkVXLAN, true); err != nil {
			return nil, err
		}
	}
	if err := n.disableEUIAutogeneration(vxlanName); err != nil {
		return nil, err
	}

	return &netlinkVXLAN, nil
}

func (n *NetlinkManager) disableEUIAutogeneration(intfName string) error {
	fileName := fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/addr_gen_mode", intfName)
	file, err := os.OpenFile(fileName, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.WriteString("1\n"); err != nil {
		return err
	}
	return nil
}

func (n *NetlinkManager) createLink(vethName string, peerName string, masterIdx int, mtu int, generateEUI bool) (*netlink.Veth, error) {
	netlinkVeth := netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name:        vethName,
			MasterIndex: masterIdx,
			MTU:         mtu,
		},
		PeerName: peerName,
	}
	if err := netlink.LinkAdd(&netlinkVeth); err != nil {
		return nil, err
	}

	if !generateEUI {
		if err := n.disableEUIAutogeneration(vethName); err != nil {
			return nil, err
		}
		if err := n.disableEUIAutogeneration(peerName); err != nil {
			return nil, err
		}
	}

	return &netlinkVeth, nil
}

func (n *NetlinkManager) setUp(intfName string) error {
	link, err := netlink.LinkByName(intfName)
	if err != nil {
		return err
	}
	return netlink.LinkSetUp(link)
}

func generateUnderlayMAC() (net.HardwareAddr, error) {
	_, vxlanIP, err := getInterfaceAndIP(UNDERLAY_LOOPBACK)
	if err != nil {
		return nil, err
	}

	generatedMac, err := generateMAC(vxlanIP)
	if err != nil {
		return nil, err
	}
	return generatedMac, nil
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
