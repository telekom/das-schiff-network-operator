package nl

import (
	"bytes"
	"fmt"
	"net"
	"os"

	"github.com/vishvananda/netlink"
)

type Layer2Information struct {
	VlanID int
	MTU    int
	VNI    int
	VRF    string

	AnycastMAC         *net.HardwareAddr
	AnycastGateways    []*netlink.Addr
	AdvertiseNeighbors bool

	CreateMACVLANInterface bool

	bridge        *netlink.Bridge
	vxlan         *netlink.Vxlan
	macvlanBridge *netlink.Veth
	macvlanHost   *netlink.Veth
}

func (n *NetlinkManager) ParseIPAddresses(addresses []string) ([]*netlink.Addr, error) {
	var addrs []*netlink.Addr
	for _, ip := range addresses {
		addr, err := netlink.ParseAddr(ip)
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func (n *NetlinkManager) CreateL2(info Layer2Information) error {
	masterIdx := -1
	if len(info.VRF) > 0 {
		l3Info, err := n.GetL3ByName(info.VRF)
		if err != nil {
			return err
		}
		masterIdx = l3Info.vrfId
	}

	if len(info.AnycastGateways) > 0 && info.AnycastMAC == nil {
		return fmt.Errorf("anycastGateways require anycastMAC to be set")
	}

	bridge, err := n.createBridge(fmt.Sprintf("%s%d", LAYER2_PREFIX, info.VlanID), masterIdx, info.MTU)
	if err != nil {
		return err
	}
	if err := n.setUp(bridge.Name); err != nil {
		return err
	}
	info.bridge = bridge

	for _, addr := range info.AnycastGateways {
		err = netlink.AddrAdd(bridge, addr)
		if err != nil {
			return err
		}
	}

	vxlan, err := n.createVXLAN(
		fmt.Sprintf("%s%d", VXLAN_PREFIX, info.VNI),
		bridge.Attrs().Index,
		info.AnycastMAC,
		info.VNI,
		info.MTU,
		false,
	)
	if err != nil {
		return err
	}
	if err := n.setUp(vxlan.Name); err != nil {
		return err
	}
	info.vxlan = vxlan

	if info.CreateMACVLANInterface {
		_, err := n.createLink(
			fmt.Sprintf("%s%d", VETH_L2_PREFIX, info.VlanID),
			fmt.Sprintf("%s%d", MACVLAN_PREFIX, info.VlanID),
			bridge.Attrs().Index,
			info.MTU,
			false,
		)
		if err != nil {
			return err
		}
		if err := n.setUp(fmt.Sprintf("%s%d", VETH_L2_PREFIX, info.VlanID)); err != nil {
			return err
		}
		if err := n.setUp(fmt.Sprintf("%s%d", MACVLAN_PREFIX, info.VlanID)); err != nil {
			return err
		}
	}

	return nil
}

func (n *NetlinkManager) CleanupL2(info Layer2Information) []error {
	errors := []error{}
	if info.vxlan != nil {
		if err := netlink.LinkDel(info.vxlan); err != nil {
			errors = append(errors, err)
		}
	}
	if info.bridge != nil {
		if err := netlink.LinkDel(info.bridge); err != nil {
			errors = append(errors, err)
		}
	}
	if info.CreateMACVLANInterface && info.macvlanBridge != nil {
		if err := netlink.LinkDel(info.macvlanBridge); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

func containsNetlinkAddress(list []*netlink.Addr, addr *netlink.Addr) bool {
	for _, v := range list {
		if v.Equal(*addr) {
			return true
		}
	}
	return false
}

func (n *NetlinkManager) reconcileIPAddresses(intf netlink.Link, current []*netlink.Addr, desired []*netlink.Addr) error {
	for _, addr := range desired {
		if !containsNetlinkAddress(current, addr) {
			if err := netlink.AddrAdd(intf, addr); err != nil {
				return fmt.Errorf("error adding desired IP address: %v", err)
			}
		}
	}
	for _, addr := range current {
		if !containsNetlinkAddress(desired, addr) {
			if err := netlink.AddrDel(intf, addr); err != nil {
				return fmt.Errorf("error removing IP address: %v", err)
			}
		}
	}
	return nil
}

func (n *NetlinkManager) ReconcileL2(current Layer2Information, desired Layer2Information) error {
	if len(desired.AnycastGateways) > 0 && desired.AnycastMAC == nil {
		return fmt.Errorf("anycastGateways require anycastMAC to be set")
	}

	// Set MTU
	if err := netlink.LinkSetMTU(current.bridge, desired.MTU); err != nil {
		return fmt.Errorf("error setting bridge MTU: %v", err)
	}
	if err := netlink.LinkSetMTU(current.vxlan, desired.MTU); err != nil {
		return fmt.Errorf("error setting vxlan MTU: %v", err)
	}
	if current.CreateMACVLANInterface {
		if err := netlink.LinkSetMTU(current.macvlanBridge, desired.MTU); err != nil {
			return fmt.Errorf("error setting veth bridge side MTU: %v", err)
		}
		if err := netlink.LinkSetMTU(current.macvlanHost, desired.MTU); err != nil {
			return fmt.Errorf("error setting veth macvlan side MTU: %v", err)
		}
	}

	// Set MAC address
	if desired.AnycastMAC != nil && !bytes.Equal(*current.AnycastMAC, *desired.AnycastMAC) {
		if err := netlink.LinkSetHardwareAddr(current.bridge, *desired.AnycastMAC); err != nil {
			return fmt.Errorf("error setting bridge mac address: %v", err)
		}
	}

	// Add/Remove macvlan Interface
	if current.CreateMACVLANInterface && !desired.CreateMACVLANInterface {
		if err := netlink.LinkDel(current.macvlanBridge); err != nil {
			return fmt.Errorf("error deleting MACVLAN interface: %v", err)
		}
	} else if !current.CreateMACVLANInterface && desired.CreateMACVLANInterface {
		_, err := n.createLink(
			fmt.Sprintf("%s%d", VETH_L2_PREFIX, current.VlanID),
			fmt.Sprintf("%s%d", MACVLAN_PREFIX, current.VlanID),
			current.bridge.Attrs().Index,
			desired.MTU,
			false,
		)
		if err != nil {
			return fmt.Errorf("error creating MACVLAN interface: %v", err)
		}
	}

	// Ensure bridge can receive gratitious ARP
	if err := os.WriteFile(fmt.Sprintf("/proc/sys/net/ipv4/conf/%s%d/arp_accept", LAYER2_PREFIX, current.VlanID), []byte("1"), 0o644); err != nil {
		return fmt.Errorf("error setting arp_accept = 1 for interface: %v", err)
	}

	// Add/Remove anycast gateways
	return n.reconcileIPAddresses(current.bridge, current.AnycastGateways, desired.AnycastGateways)
}

func (n *NetlinkManager) ListL2() ([]Layer2Information, error) {
	return n.listL2()
}

func (n *NetlinkManager) GetBridgeId(info Layer2Information) (int, error) {
	bridgeName := fmt.Sprintf("%s%d", LAYER2_PREFIX, info.VlanID)
	link, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return -1, err
	}
	return link.Attrs().Index, nil
}
