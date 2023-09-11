package nl

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
)

const (
	interfaceConfigTimeout = 500 * time.Millisecond
	neighFilePermissions   = 0o600
)

type Layer2Information struct {
	VlanID                 int
	MTU                    int
	VNI                    int
	VRF                    string
	AnycastMAC             *net.HardwareAddr
	AnycastGateways        []*netlink.Addr
	AdvertiseNeighbors     bool
	NeighSuppression       *bool
	CreateMACVLANInterface bool
	bridge                 *netlink.Bridge
	vxlan                  *netlink.Vxlan
	macvlanBridge          *netlink.Veth
	macvlanHost            *netlink.Veth
}

type NeighborInformation struct {
	Interface string
	State     string
	Family    string
	IP        string
	MAC       string
}

func getNeighborState(state int) (string, error) {
	switch state {
	case netlink.NUD_DELAY:
		return "delay", nil
	case netlink.NUD_FAILED:
		return "failed", nil
	case netlink.NUD_INCOMPLETE:
		return "incomplete", nil
	case netlink.NUD_NOARP:
		return "no_arp", nil
	case netlink.NUD_NONE:
		return "none", nil
	case netlink.NUD_PERMANENT:
		return "permanent", nil
	case netlink.NUD_PROBE:
		return "probe", nil
	case netlink.NUD_REACHABLE:
		return "reachable", nil
	case netlink.NUD_STALE:
		return "stale", nil
	default:
		return "", fmt.Errorf("[%x] is not a valid neighbor state", state)
	}
}

func (*NetlinkManager) ParseIPAddresses(addresses []string) ([]*netlink.Addr, error) {
	addrs := []*netlink.Addr{}
	for _, ip := range addresses {
		addr, err := netlink.ParseAddr(ip)
		if err != nil {
			return nil, fmt.Errorf("error while parsing IP address: %w", err)
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func (n *NetlinkManager) CreateL2(info *Layer2Information) error {
	masterIdx := -1
	if len(info.VRF) > 0 {
		l3Info, err := n.GetL3ByName(info.VRF)
		if err != nil {
			return err
		}
		masterIdx = l3Info.vrfID
	}

	if len(info.AnycastGateways) > 0 && info.AnycastMAC == nil {
		return fmt.Errorf("anycastGateways require anycastMAC to be set")
	}

	bridge, err := n.setupBridge(info, masterIdx)
	if err != nil {
		return err
	}

	return n.setupVXLAN(info, bridge)
}

func (n *NetlinkManager) setupBridge(info *Layer2Information, masterIdx int) (*netlink.Bridge, error) {
	bridge, err := n.createBridge(fmt.Sprintf("%s%d", layer2Prefix, info.VlanID), info.AnycastMAC, masterIdx, info.MTU)
	if err != nil {
		return nil, err
	}
	if err := n.setUp(bridge.Name); err != nil {
		return nil, err
	}
	info.bridge = bridge
	if err := n.configureBridge(bridge.Name); err != nil {
		return nil, err
	}

	// Wait 500ms before configuring anycast gateways on newly added interface
	time.Sleep(interfaceConfigTimeout)
	for _, addr := range info.AnycastGateways {
		err = netlink.AddrAdd(bridge, addr)
		if err != nil {
			return nil, fmt.Errorf("error while adding address: %w", err)
		}
	}

	return bridge, nil
}

func (n *NetlinkManager) setupVXLAN(info *Layer2Information, bridge *netlink.Bridge) error {
	neighSuppression := os.Getenv("NWOP_NEIGH_SUPPRESSION") == "true"
	if len(info.AnycastGateways) == 0 {
		neighSuppression = false
	}
	if info.NeighSuppression != nil {
		neighSuppression = *info.NeighSuppression
	}
	vxlan, err := n.createVXLAN(
		fmt.Sprintf("%s%d", vxlanPrefix, info.VNI),
		bridge.Attrs().Index,
		info.VNI,
		info.MTU,
		false,
		neighSuppression,
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
			fmt.Sprintf("%s%d", vethL2Prefix, info.VlanID),
			fmt.Sprintf("%s%d", macvlanPrefix, info.VlanID),
			bridge.Attrs().Index,
			info.MTU,
			false,
		)
		if err != nil {
			return err
		}
		if err := n.setUp(fmt.Sprintf("%s%d", vethL2Prefix, info.VlanID)); err != nil {
			return err
		}
		if err := n.setUp(fmt.Sprintf("%s%d", macvlanPrefix, info.VlanID)); err != nil {
			return err
		}
	}

	return nil
}

func (*NetlinkManager) CleanupL2(info *Layer2Information) []error {
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

func (*NetlinkManager) reconcileIPAddresses(intf netlink.Link, current, desired []*netlink.Addr) error {
	for _, addr := range desired {
		if !containsNetlinkAddress(current, addr) {
			if err := netlink.AddrAdd(intf, addr); err != nil {
				return fmt.Errorf("error adding desired IP address: %w", err)
			}
		}
	}
	for _, addr := range current {
		if !containsNetlinkAddress(desired, addr) {
			if err := netlink.AddrDel(intf, addr); err != nil {
				return fmt.Errorf("error removing IP address: %w", err)
			}
		}
	}
	return nil
}

func (n *NetlinkManager) ReconcileL2(current, desired *Layer2Information) error {
	if len(desired.AnycastGateways) > 0 && desired.AnycastMAC == nil {
		return fmt.Errorf("anycastGateways require anycastMAC to be set")
	}

	if err := setMtU(current, desired); err != nil {
		return err
	}

	vxlanMAC, err := generateUnderlayMAC()
	if err != nil {
		return fmt.Errorf("error generating MAC for vxlan device: %w", err)
	}

	if err := setHardwareAddresses(current, desired, vxlanMAC); err != nil {
		return err
	}

	// Reconcile VRF
	shouldReattachL2VNI, err := n.isL2VNIreattachRequired(current, desired)
	if err != nil {
		return err
	}

	if shouldReattachL2VNI {
		if err := reattachL2VNI(current); err != nil {
			return err
		}
	}

	// Add/Remove macvlan Interface
	if err := n.setupMACVLANinterface(current, desired); err != nil {
		return err
	}

	if err := n.configureBridge(fmt.Sprintf("%s%d", layer2Prefix, current.VlanID)); err != nil {
		return err
	}

	if err := doNeighSuppression(current, desired); err != nil {
		return err
	}

	// Add/Remove anycast gateways
	return n.reconcileIPAddresses(current.bridge, current.AnycastGateways, desired.AnycastGateways)
}

func setMtU(current, desired *Layer2Information) error {
	// Set MTU
	if err := netlink.LinkSetMTU(current.bridge, desired.MTU); err != nil {
		return fmt.Errorf("error setting bridge MTU: %w", err)
	}
	if err := netlink.LinkSetMTU(current.vxlan, desired.MTU); err != nil {
		return fmt.Errorf("error setting vxlan MTU: %w", err)
	}
	if current.CreateMACVLANInterface {
		if err := netlink.LinkSetMTU(current.macvlanBridge, desired.MTU); err != nil {
			return fmt.Errorf("error setting veth bridge side MTU: %w", err)
		}
		if err := netlink.LinkSetMTU(current.macvlanHost, desired.MTU); err != nil {
			return fmt.Errorf("error setting veth macvlan side MTU: %w", err)
		}
	}
	return nil
}

func setHardwareAddresses(current, desired *Layer2Information, vxlanMAC net.HardwareAddr) error {
	if desired.AnycastMAC != nil && !bytes.Equal(current.bridge.HardwareAddr, *desired.AnycastMAC) {
		if err := netlink.LinkSetDown(current.vxlan); err != nil {
			return fmt.Errorf("error downing vxlan before changing MAC: %w", err)
		}
		time.Sleep(interfaceConfigTimeout) // Wait for FRR to pickup interface down
		if err := netlink.LinkSetHardwareAddr(current.bridge, *desired.AnycastMAC); err != nil {
			return fmt.Errorf("error setting vxlan mac address: %w", err)
		}
		time.Sleep(interfaceConfigTimeout)
		if err := netlink.LinkSetUp(current.vxlan); err != nil {
			return fmt.Errorf("error upping vxlan after changing MAC: %w", err)
		}
	}
	if !bytes.Equal(current.vxlan.HardwareAddr, vxlanMAC) {
		if err := netlink.LinkSetHardwareAddr(current.vxlan, vxlanMAC); err != nil {
			return fmt.Errorf("error setting vxlan mac address: %w", err)
		}
	}
	return nil
}

func reattachL2VNI(current *Layer2Information) error {
	// First set VXLAN down and detach from L2VNI bridge
	if err := netlink.LinkSetDown(current.vxlan); err != nil {
		return fmt.Errorf("error downing vxlan before changing MAC: %w", err)
	}
	if err := netlink.LinkSetNoMaster(current.vxlan); err != nil {
		return fmt.Errorf("error removing vxlan from bridge before changing MAC: %w", err)
	}

	// Reattach VXLAN to L2VNI bridge
	if err := netlink.LinkSetMaster(current.vxlan, current.bridge); err != nil {
		return fmt.Errorf("error adding vxlan to bridge after changing MAC: %w", err)
	}

	// Disable learning on bridgeport
	if err := netlink.LinkSetLearning(current.vxlan, false); err != nil {
		return fmt.Errorf("error setting vxlan learning to false: %w", err)
	}

	// Up VXLAN interface again
	if err := netlink.LinkSetUp(current.vxlan); err != nil {
		return fmt.Errorf("error uping vxlan after changing MAC: %w", err)
	}
	return nil
}

func doNeighSuppression(current, desired *Layer2Information) error {
	neighSuppression := os.Getenv("NWOP_NEIGH_SUPPRESSION") == "true"
	if len(desired.AnycastGateways) == 0 {
		neighSuppression = false
	}
	if desired.NeighSuppression != nil {
		neighSuppression = *desired.NeighSuppression
	}
	return setNeighSuppression(current.vxlan, neighSuppression)
}

func (n *NetlinkManager) isL2VNIreattachRequired(current, desired *Layer2Information) (bool, error) {
	shouldReattachL2VNI := false
	// Reconcile VRF
	if current.VRF != desired.VRF {
		shouldReattachL2VNI = true
		if len(desired.VRF) > 0 {
			l3Info, err := n.GetL3ByName(desired.VRF)
			if err != nil {
				return shouldReattachL2VNI, fmt.Errorf("error while getting L3 by name: %w", err)
			}
			if err := netlink.LinkSetMasterByIndex(current.bridge, l3Info.vrfID); err != nil {
				return shouldReattachL2VNI, fmt.Errorf("error while setting master by index: %w", err)
			}
		} else {
			if err := netlink.LinkSetNoMaster(current.bridge); err != nil {
				return shouldReattachL2VNI, fmt.Errorf("error while trying to link set no master: %w", err)
			}
		}
	}

	protinfo, err := netlink.LinkGetProtinfo(current.vxlan)
	if err != nil {
		return shouldReattachL2VNI, fmt.Errorf("error getting bridge port info: %w", err)
	}
	if protinfo.Learning {
		shouldReattachL2VNI = true
	}

	return shouldReattachL2VNI, nil
}

func (n *NetlinkManager) setupMACVLANinterface(current, desired *Layer2Information) error {
	if current.CreateMACVLANInterface && !desired.CreateMACVLANInterface {
		if err := netlink.LinkDel(current.macvlanBridge); err != nil {
			return fmt.Errorf("error deleting MACVLAN interface: %w", err)
		}
	} else if !current.CreateMACVLANInterface && desired.CreateMACVLANInterface {
		_, err := n.createLink(
			fmt.Sprintf("%s%d", vethL2Prefix, current.VlanID),
			fmt.Sprintf("%s%d", macvlanPrefix, current.VlanID),
			current.bridge.Attrs().Index,
			desired.MTU,
			false,
		)
		if err != nil {
			return fmt.Errorf("error creating MACVLAN interface: %w", err)
		}
	}
	return nil
}

func (*NetlinkManager) configureBridge(intfName string) error {
	// Ensure bridge can receive gratitious ARP
	if err := os.WriteFile(fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/arp_accept", intfName), []byte("1"), neighFilePermissions); err != nil {
		return fmt.Errorf("error setting arp_accept = 1 for interface: %w", err)
	}

	baseTimer := os.Getenv("NWOP_NEIGH_BASE_REACHABLE_TIME")
	if baseTimer == "" {
		baseTimer = "30000"
	}

	// Ensure Ipv4 Neighbor expiry is set to 30min
	if err := os.WriteFile(fmt.Sprintf("/proc/sys/net/ipv4/neigh/%s/base_reachable_time_ms", intfName), []byte(baseTimer), neighFilePermissions); err != nil {
		return fmt.Errorf("error setting ipv4 base_reachable_time_ms = %s for interface: %w", baseTimer, err)
	}
	// Ensure IPv6 Neighbor expiry is set to 30min
	if err := os.WriteFile(fmt.Sprintf("/proc/sys/net/ipv6/neigh/%s/base_reachable_time_ms", intfName), []byte(baseTimer), neighFilePermissions); err != nil {
		return fmt.Errorf("error setting ipv6 base_reachable_time_ms = %s for interface: %w", baseTimer, err)
	}
	return nil
}

func (n *NetlinkManager) ListNeighborInformation() ([]NeighborInformation, error) {
	netlinkNeighbors, err := n.listNeighbors()
	if err != nil {
		return nil, err
	}
	neighbors := []NeighborInformation{}
	for index := range netlinkNeighbors {
		family, err := GetAddressFamily(netlinkNeighbors[index].Family)
		if err != nil {
			return nil, fmt.Errorf("error converting addressFamily [%d]: %w", &netlinkNeighbors[index].Family, err)
		}
		state, err := getNeighborState(netlinkNeighbors[index].State)
		if err != nil {
			return nil, fmt.Errorf("error converting neighborState [%d]: %w", &netlinkNeighbors[index].State, err)
		}

		linkInfo, err := netlink.LinkByIndex(netlinkNeighbors[index].LinkIndex)
		if err != nil {
			return nil, fmt.Errorf("error getting link by index: %w", err)
		}
		interfaceName := linkInfo.Attrs().Name
		hardwareAddr := netlinkNeighbors[index].HardwareAddr.String()
		// This ensures that only neighbors of secondary interfaces are imported
		// or hardware interfaces which support VFs
		if strings.HasPrefix(interfaceName, vethL2Prefix) ||
			strings.HasPrefix(interfaceName, macvlanPrefix) ||
			strings.HasPrefix(interfaceName, layer2Prefix) ||
			linkInfo.Attrs().Vfs != nil ||
			netlinkNeighbors[index].State != netlink.NUD_NOARP {
			neighbor := NeighborInformation{
				Family:    family,
				State:     state,
				MAC:       hardwareAddr,
				IP:        netlinkNeighbors[index].IP.String(),
				Interface: interfaceName,
			}
			neighbors = append(neighbors, neighbor)
		}
	}
	return neighbors, nil
}

func (*NetlinkManager) GetBridgeID(info *Layer2Information) (int, error) {
	bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)
	link, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return -1, fmt.Errorf("error while getting link by name: %w", err)
	}
	return link.Attrs().Index, nil
}
