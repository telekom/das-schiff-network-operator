package nl

import (
	"fmt"
	"net"
	"os"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

const (
	iflaBrPortNeighSuppress = 32
	hwAddrByteSize          = 6
)

func (n *NetlinkManager) createVRF(vrfName string, table int) (*netlink.Vrf, error) {
	netlinkVrf := netlink.Vrf{
		LinkAttrs: netlink.LinkAttrs{
			Name: vrfName,
		},
		Table: uint32(table),
	}

	if err := netlink.LinkAdd(&netlinkVrf); err != nil {
		return nil, fmt.Errorf("error adding link: %w", err)
	}
	if err := n.disableEUIAutogeneration(vrfName); err != nil {
		return nil, err
	}
	if err := netlink.LinkSetUp(&netlinkVrf); err != nil {
		return nil, fmt.Errorf("error setting link up: %w", err)
	}

	return &netlinkVrf, nil
}

func (n *NetlinkManager) createBridge(bridgeName string, macAddress *net.HardwareAddr, masterIdx, mtu int) (*netlink.Bridge, error) {
	netlinkBridge := netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name: bridgeName,
			MTU:  mtu,
		},
	}
	if masterIdx != -1 {
		netlinkBridge.LinkAttrs.MasterIndex = masterIdx
	}
	if macAddress != nil {
		netlinkBridge.LinkAttrs.HardwareAddr = *macAddress
	}

	if err := netlink.LinkAdd(&netlinkBridge); err != nil {
		return nil, fmt.Errorf("error adding link: %w", err)
	}
	if err := n.disableEUIAutogeneration(bridgeName); err != nil {
		return nil, fmt.Errorf("error disabling EUI autogeneration: %w", err)
	}

	return &netlinkBridge, nil
}

func (n *NetlinkManager) createVXLAN(vxlanName string, bridgeIdx, vni, mtu int, hairpin, neighSuppression bool) (*netlink.Vxlan, error) {
	vxlanIf, vxlanIP, err := getInterfaceAndIP(underlayLoopback)
	if err != nil {
		return nil, err
	}

	generatedMac, err := generateMAC(vxlanIP)
	if err != nil {
		return nil, err
	}

	netlinkVXLAN := netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:         vxlanName,
			MasterIndex:  bridgeIdx,
			MTU:          mtu,
			HardwareAddr: generatedMac,
		},
		VxlanId:      vni,
		VtepDevIndex: vxlanIf,
		SrcAddr:      vxlanIP,
		Learning:     false,
		Port:         vxlanPort,
	}
	if err := netlink.LinkAdd(&netlinkVXLAN); err != nil {
		return nil, fmt.Errorf("error adding link: %w", err)
	}
	if err := netlink.LinkSetLearning(&netlinkVXLAN, false); err != nil {
		return nil, fmt.Errorf("error disabling link learning: %w", err)
	}
	if err := setNeighSuppression(&netlinkVXLAN, neighSuppression); err != nil {
		return nil, err
	}
	if hairpin {
		if err := netlink.LinkSetHairpin(&netlinkVXLAN, true); err != nil {
			return nil, fmt.Errorf("error setting link's hairpin mode: %w", err)
		}
	}
	if err := n.disableEUIAutogeneration(vxlanName); err != nil {
		return nil, err
	}

	return &netlinkVXLAN, nil
}

func (*NetlinkManager) disableEUIAutogeneration(intfName string) error {
	fileName := fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/addr_gen_mode", intfName)
	file, err := os.OpenFile(fileName, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()
	if _, err := file.WriteString("1\n"); err != nil {
		return fmt.Errorf("error writing to file: %w", err)
	}
	return nil
}

func (n *NetlinkManager) createLink(vethName, peerName string, masterIdx, mtu int, generateEUI bool) (*netlink.Veth, error) {
	netlinkVeth := netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name:        vethName,
			MasterIndex: masterIdx,
			MTU:         mtu,
		},
		PeerName: peerName,
	}
	if err := netlink.LinkAdd(&netlinkVeth); err != nil {
		return nil, fmt.Errorf("error adding link: %w", err)
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

func (*NetlinkManager) setUp(intfName string) error {
	link, err := netlink.LinkByName(intfName)
	if err != nil {
		return fmt.Errorf("error getting link by name: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("error setting link up: %w", err)
	}
	return nil
}

func generateUnderlayMAC() (net.HardwareAddr, error) {
	_, vxlanIP, err := getInterfaceAndIP(underlayLoopback)
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
		return -1, nil, fmt.Errorf("error listing link's addresses: %w", err)
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
	hwaddr := make([]byte, hwAddrByteSize)
	copy(hwaddr, macPrefix)
	copy(hwaddr[2:], ip.To4())
	return hwaddr, nil
}

func setNeighSuppression(link netlink.Link, mode bool) error {
	req := nl.NewNetlinkRequest(unix.RTM_SETLINK, unix.NLM_F_ACK)

	msg := nl.NewIfInfomsg(unix.AF_BRIDGE)
	msg.Index = int32(link.Attrs().Index)
	req.AddData(msg)

	br := nl.NewRtAttr(unix.IFLA_PROTINFO|unix.NLA_F_NESTED, nil)
	br.AddRtAttr(iflaBrPortNeighSuppress, boolToByte(mode))
	req.AddData(br)
	_, err := req.Execute(unix.NETLINK_ROUTE, 0)
	if err != nil {
		return fmt.Errorf("error executing request: %w", err)
	}
	return nil
}

func boolToByte(x bool) []byte {
	if x {
		return []byte{1}
	}
	return []byte{0}
}
