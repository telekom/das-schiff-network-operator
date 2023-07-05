package nl

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"time"

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

	bridge, err := n.createBridge(fmt.Sprintf("%s%d", LAYER2_PREFIX, info.VlanID), info.AnycastMAC, masterIdx, info.MTU)
	if err != nil {
		return err
	}
	if err := n.setUp(bridge.Name); err != nil {
		return err
	}
	info.bridge = bridge
	if err := n.configureBridge(bridge.Name); err != nil {
		return err
	}
	// Wait 500ms before configuring anycast gateways on newly added interface
	time.Sleep(500 * time.Millisecond)
	for _, addr := range info.AnycastGateways {
		err = netlink.AddrAdd(bridge, addr)
		if err != nil {
			return err
		}
	}

	vxlan, err := n.createVXLAN(
		fmt.Sprintf("%s%d", VXLAN_PREFIX, info.VNI),
		bridge.Attrs().Index,
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

	vxlanMAC, err := generateUnderlayMAC()
	if err != nil {
		return fmt.Errorf("error generating MAC for vxlan device: %v", err)
	}

	if desired.AnycastMAC != nil && !bytes.Equal(current.bridge.HardwareAddr, *desired.AnycastMAC) {
		if err := netlink.LinkSetDown(current.vxlan); err != nil {
			return fmt.Errorf("error downing vxlan before changing MAC: %v", err)
		}
		time.Sleep(500 * time.Millisecond) // Wait for FRR to pickup interface down
		if err := netlink.LinkSetHardwareAddr(current.bridge, *desired.AnycastMAC); err != nil {
			return fmt.Errorf("error setting vxlan mac address: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
		if err := netlink.LinkSetUp(current.vxlan); err != nil {
			return fmt.Errorf("error upping vxlan after changing MAC: %v", err)
		}
	}
	if !bytes.Equal(current.vxlan.HardwareAddr, vxlanMAC) {
		if err := netlink.LinkSetHardwareAddr(current.vxlan, vxlanMAC); err != nil {
			return fmt.Errorf("error setting vxlan mac address: %v", err)
		}
	}

	reattachL2VNI := false
	// Reconcile VRF
	if current.VRF != desired.VRF {
		reattachL2VNI = true
		if len(desired.VRF) > 0 {
			l3Info, err := n.GetL3ByName(desired.VRF)
			if err != nil {
				return err
			}
			if err := netlink.LinkSetMasterByIndex(current.bridge, l3Info.vrfId); err != nil {
				return err
			}
		} else {
			if err := netlink.LinkSetNoMaster(current.bridge); err != nil {
				return err
			}
		}
	}

	protinfo, err := netlink.LinkGetProtinfo(current.vxlan)
	if err != nil {
		return fmt.Errorf("error getting bridge port info: %v", err)
	}
	if protinfo.Learning {
		reattachL2VNI = true
	}

	if reattachL2VNI {
		// First set VXLAN down and detach from L2VNI bridge
		if err := netlink.LinkSetDown(current.vxlan); err != nil {
			return fmt.Errorf("error downing vxlan before changing MAC: %v", err)
		}
		if err := netlink.LinkSetNoMaster(current.vxlan); err != nil {
			return fmt.Errorf("error removing vxlan from bridge before changing MAC: %v", err)
		}

		// Reattach VXLAN to L2VNI bridge
		if err := netlink.LinkSetMaster(current.vxlan, current.bridge); err != nil {
			return fmt.Errorf("error adding vxlan to bridge after changing MAC: %v", err)
		}

		// Disable learning on bridgeport
		if err := netlink.LinkSetLearning(current.vxlan, false); err != nil {
			return fmt.Errorf("error setting vxlan learning to false: %v", err)
		}

		// Up VXLAN interface again
		if err := netlink.LinkSetUp(current.vxlan); err != nil {
			return fmt.Errorf("error uping vxlan after changing MAC: %v", err)
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

	if err := n.configureBridge(fmt.Sprintf("%s%d", LAYER2_PREFIX, current.VlanID)); err != nil {
		return err
	}
	if err := setNeighSuppression(current.vxlan, os.Getenv("NWOP_NEIGH_SUPPRESSION") == "true"); err != nil {
		return err
	}

	// Add/Remove anycast gateways
	return n.reconcileIPAddresses(current.bridge, current.AnycastGateways, desired.AnycastGateways)
}

func (n *NetlinkManager) configureBridge(intfName string) error {
	// Ensure bridge can receive gratitious ARP
	if err := os.WriteFile(fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/arp_accept", intfName), []byte("1"), 0o644); err != nil {
		return fmt.Errorf("error setting arp_accept = 1 for interface: %v", err)
	}

	baseTimer := os.Getenv("NWOP_NEIGH_BASE_REACHABLE_TIME")
	if baseTimer == "" {
		baseTimer = "30000"
	}

	// Ensure Ipv4 Neighbor expiry is set to 30min
	if err := os.WriteFile(fmt.Sprintf("/proc/sys/net/ipv4/neigh/%s/base_reachable_time_ms", intfName), []byte(baseTimer), 0o644); err != nil {
		return fmt.Errorf("error setting ipv4 base_reachable_time_ms = %s for interface: %v", baseTimer, err)
	}
	// Ensure IPv6 Neighbor expiry is set to 30min
	if err := os.WriteFile(fmt.Sprintf("/proc/sys/net/ipv6/neigh/%s/base_reachable_time_ms", intfName), []byte(baseTimer), 0o644); err != nil {
		return fmt.Errorf("error setting ipv6 base_reachable_time_ms = %s for interface: %v", baseTimer, err)
	}
	return nil
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
