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

func (n *Manager) createVRF(vrfName string, table int) (*netlink.Vrf, error) {
	netlinkVrf := netlink.Vrf{
		LinkAttrs: netlink.LinkAttrs{
			Name: vrfName,
		},
		Table: uint32(table), //nolint:gosec
	}

	if err := n.toolkit.LinkAdd(&netlinkVrf); err != nil {
		return nil, fmt.Errorf("error adding vrf link: %w", err)
	}
	if err := n.setEUIAutogeneration(vrfName, false); err != nil {
		return nil, err
	}
	if err := n.toolkit.LinkSetUp(&netlinkVrf); err != nil {
		return nil, fmt.Errorf("error setting link up: %w", err)
	}

	return &netlinkVrf, nil
}

func (n *Manager) createBridge(bridgeName string, macAddress *net.HardwareAddr, masterIdx, mtu int, underlayRMAC, assignEUI bool) (*netlink.Bridge, error) {
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
	} else if underlayRMAC {
		_, vxlanIP, err := n.getUnderlayInterfaceAndIP()
		if err != nil {
			return nil, err
		}

		generatedMac, err := generateMAC(vxlanIP)
		if err != nil {
			return nil, err
		}
		netlinkBridge.LinkAttrs.HardwareAddr = generatedMac
	}

	if err := n.toolkit.LinkAdd(&netlinkBridge); err != nil {
		return nil, fmt.Errorf("error adding bridge link: %w", err)
	}
	if err := n.setEUIAutogeneration(bridgeName, assignEUI); err != nil {
		return nil, fmt.Errorf("error disabling EUI autogeneration: %w", err)
	}

	return &netlinkBridge, nil
}

func (n *Manager) createVXLAN(vxlanName string, bridgeIdx, vni, mtu int, hairpin, neighSuppression bool) (*netlink.Vxlan, error) {
	vxlanIf, vxlanIP, err := n.getUnderlayInterfaceAndIP()
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
	if err := n.toolkit.LinkAdd(&netlinkVXLAN); err != nil {
		return nil, fmt.Errorf("error adding vxlan link: %w", err)
	}
	if err := n.toolkit.LinkSetLearning(&netlinkVXLAN, false); err != nil {
		return nil, fmt.Errorf("error disabling link learning: %w", err)
	}
	if err := n.setNeighSuppression(&netlinkVXLAN, neighSuppression); err != nil {
		return nil, err
	}
	if hairpin {
		if err := n.toolkit.LinkSetHairpin(&netlinkVXLAN, true); err != nil {
			return nil, fmt.Errorf("error setting link's hairpin mode: %w", err)
		}
	}
	if err := n.setEUIAutogeneration(vxlanName, false); err != nil {
		return nil, err
	}

	return &netlinkVXLAN, nil
}

func (*Manager) setEUIAutogeneration(intfName string, generateEUI bool) error {
	fileName := fmt.Sprintf("%s/ipv6/conf/%s/addr_gen_mode", procSysNetPath, intfName)
	file, err := os.OpenFile(fileName, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()
	value := "1"
	if generateEUI {
		value = "0"
	}
	if _, err := fmt.Fprintf(file, "%s\n", value); err != nil {
		return fmt.Errorf("error writing to file: %w", err)
	}
	return nil
}

func (n *Manager) createVLAN(vlanID, masterIdx, mtu int) (*netlink.Vlan, error) {
	hbrInterface, err := n.toolkit.LinkByName(n.baseConfig.TrunkInterfaceName)
	if err != nil {
		return nil, fmt.Errorf("error getting link by name: %w", err)
	}

	vlanName := fmt.Sprintf("%s%d", vlanPrefix, vlanID)

	netlinkVLAN := netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        vlanName,
			MasterIndex: masterIdx,
			ParentIndex: hbrInterface.Attrs().Index,
			MTU:         mtu,
		},
		VlanId: vlanID,
	}

	if err := n.toolkit.LinkAdd(&netlinkVLAN); err != nil {
		return nil, fmt.Errorf("error adding link: %w", err)
	}

	if err := n.setEUIAutogeneration(vlanName, false); err != nil {
		return nil, err
	}

	return &netlinkVLAN, nil
}

func (n *Manager) setUp(intfName string) error {
	link, err := n.toolkit.LinkByName(intfName)
	if err != nil {
		return fmt.Errorf("error getting link by name: %w", err)
	}
	if err := n.toolkit.LinkSetUp(link); err != nil {
		return fmt.Errorf("error setting link up: %w", err)
	}
	return nil
}

func (n *Manager) generateUnderlayMAC() (net.HardwareAddr, error) {
	_, vxlanIP, err := n.getUnderlayInterfaceAndIP()
	if err != nil {
		return nil, err
	}

	generatedMac, err := generateMAC(vxlanIP)
	if err != nil {
		return nil, err
	}
	return generatedMac, nil
}

func (n *Manager) getUnderlayInterfaceAndIP() (int, net.IP, error) {
	dummy, err := n.toolkit.LinkByName(underlayInterfaceName)
	if err != nil {
		return -1, nil, fmt.Errorf("error getting link by name: %w", err)
	}
	address := net.ParseIP(n.baseConfig.VTEPLoopbackIP)

	return dummy.Attrs().Index, address, nil
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

func (n *Manager) setNeighSuppression(link netlink.Link, mode bool) error {
	req := nl.NewNetlinkRequest(unix.RTM_SETLINK, unix.NLM_F_ACK)

	msg := nl.NewIfInfomsg(unix.AF_BRIDGE)
	msg.Index = int32(link.Attrs().Index) //nolint:gosec
	req.AddData(msg)

	br := nl.NewRtAttr(unix.IFLA_PROTINFO|unix.NLA_F_NESTED, nil)
	br.AddRtAttr(iflaBrPortNeighSuppress, boolToByte(mode))
	req.AddData(br)
	_, err := n.toolkit.ExecuteNetlinkRequest(req, unix.NETLINK_ROUTE, 0)
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
