package nl

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"slices"
	"time"

	schiff_unix "github.com/telekom/das-schiff-network-operator/pkg/unix"
	"github.com/vishvananda/netlink"
	"golang.org/x/exp/maps"
	"golang.org/x/sys/unix"
)

const (
	interfaceConfigTimeout = 500 * time.Millisecond
	neighFilePermissions   = 0o600
)

var procSysNetPath = "/proc/sys/net"

type Layer2Information struct {
	VlanID           int      `json:"vlanID"`
	MTU              int      `json:"mtu"`
	VNI              int      `json:"vni"`
	VRF              string   `json:"vrf"`
	AnycastMAC       *string  `json:"anycastMAC"`
	AnycastGateways  []string `json:"anycastGateways"`
	NeighSuppression *bool    `json:"neighSuppression"`
	bridge           *netlink.Bridge
	vxlan            *netlink.Vxlan
	vlanInterface    *netlink.Vlan
}

type NeighborInformation struct {
	Interface string
	State     string
	Family    string
	Flag      string
	Quantity  float64
}
type NeighborKey struct {
	InterfaceIndex, State, Flags, Family int
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

func getFlags(flag int) (string, error) {
	switch flag {
	case schiff_unix.NTF_UNSPEC:
		return "", nil
	case netlink.NTF_MASTER:
		return "permanent", nil
	case netlink.NTF_ROUTER:
		return "router", nil
	case netlink.NTF_SELF:
		return "self", nil
	case netlink.NTF_PROXY:
		return "proxy", nil
	case netlink.NTF_USE:
		return "use", nil
	case unix.NTF_EXT_LEARNED:
		return "extern_learn", nil
	case unix.NTF_OFFLOADED:
		return "offloaded", nil
	default:
		return "", fmt.Errorf("cannot convert flag %x", flag)
	}
}

func (n *Manager) ParseIPAddresses(addresses []string) ([]*netlink.Addr, error) {
	addrs := []*netlink.Addr{}
	for _, ip := range addresses {
		addr, err := n.toolkit.ParseAddr(ip)
		if err != nil {
			return nil, fmt.Errorf("error while parsing IP address: %w", err)
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func (n *Manager) CreateL2(info *Layer2Information) error {
	masterIdx := -1
	if info.VRF != "" {
		vrfID, err := n.GetVRFInterfaceIdxByName(info.VRF)
		if err != nil {
			return err
		}
		masterIdx = vrfID
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

func (n *Manager) setupBridge(info *Layer2Information, masterIdx int) (*netlink.Bridge, error) {
	var macAddress *net.HardwareAddr
	if info.AnycastMAC != nil {
		mac, err := net.ParseMAC(*info.AnycastMAC)
		if err != nil {
			return nil, fmt.Errorf("error while parsing MAC address: %w", err)
		}
		macAddress = &mac
	}
	if len(info.AnycastGateways) > 0 && info.VRF == "" {
		return nil, fmt.Errorf("anycastGateways require VRF to be set")
	}

	bridge, err := n.createBridge(fmt.Sprintf("%s%d", layer2SVI, info.VlanID), macAddress, masterIdx, info.MTU, false, len(info.AnycastGateways) > 0)
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
	anycastGateways, err := n.ParseIPAddresses(info.AnycastGateways)
	if err != nil {
		return nil, fmt.Errorf("failed to parse addresses: %w", err)
	}
	for _, addr := range anycastGateways {
		err = n.toolkit.AddrAdd(bridge, addr)
		if err != nil {
			return nil, fmt.Errorf("error while adding address: %w", err)
		}
	}

	return bridge, nil
}

func (n *Manager) setupVXLAN(info *Layer2Information, bridge *netlink.Bridge) error {
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

	if _, err := n.createVLAN(
		info.VlanID,
		bridge.Attrs().Index,
		info.MTU); err != nil {
		return err
	}

	if err := n.setUp(fmt.Sprintf("%s%d", vlanPrefix, info.VlanID)); err != nil {
		return err
	}

	return nil
}

func (n *Manager) CleanupL2(info *Layer2Information) []error {
	errors := []error{}
	if info.vxlan != nil {
		if err := n.toolkit.LinkDel(info.vxlan); err != nil {
			errors = append(errors, err)
		}
	}
	if info.bridge != nil {
		if err := n.toolkit.LinkDel(info.bridge); err != nil {
			errors = append(errors, err)
		}
	}
	if info.vlanInterface != nil {
		if err := n.toolkit.LinkDel(info.vlanInterface); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

func containsNetlinkAddress(list []*netlink.Addr, addr *netlink.Addr) bool {
	for _, v := range list {
		if v.IP.Equal(addr.IP) && slices.Equal(v.Mask, addr.Mask) {
			return true
		}
	}
	return false
}

func (n *Manager) reconcileIPAddresses(intf netlink.Link, current, desired []*netlink.Addr) error {
	for _, addr := range desired {
		if !containsNetlinkAddress(current, addr) {
			if err := n.toolkit.AddrAdd(intf, addr); err != nil {
				return fmt.Errorf("error adding desired IP address: %w", err)
			}
		}
	}
	for _, addr := range current {
		if !containsNetlinkAddress(desired, addr) {
			if err := n.toolkit.AddrDel(intf, addr); err != nil {
				return fmt.Errorf("error removing IP address: %w", err)
			}
		}
	}
	return nil
}

func (n *Manager) reconcileEUIAutogeneration(intfName string, intf netlink.Link, desired []*netlink.Addr) error {
	enableEUI := len(desired) > 0
	if err := n.setEUIAutogeneration(intfName, enableEUI); err != nil {
		return fmt.Errorf("error setting EUI autogeneration: %w", err)
	}
	if !enableEUI {
		addresses, err := n.toolkit.AddrList(intf, unix.AF_INET6)
		if err != nil {
			return fmt.Errorf("error listing link's IPv6 addresses: %w", err)
		}
		for i := range addresses {
			if addresses[i].IP.IsLinkLocalUnicast() {
				if err := n.toolkit.AddrDel(intf, &addresses[i]); err != nil {
					return fmt.Errorf("error removing link local IPv6 address: %w", err)
				}
			}
		}
	}
	return nil
}

func (n *Manager) ReconcileL2(current, desired *Layer2Information) error {
	bridgeName := fmt.Sprintf("%s%d", layer2SVI, current.VlanID)
	if len(desired.AnycastGateways) > 0 && desired.AnycastMAC == nil {
		return fmt.Errorf("anycastGateways require anycastMAC to be set")
	}

	if err := n.setMTU(current, desired); err != nil {
		return err
	}

	vxlanMAC, err := n.generateUnderlayMAC()
	if err != nil {
		return fmt.Errorf("error generating MAC for vxlan device: %w", err)
	}

	if err := n.setHardwareAddresses(current, desired, vxlanMAC); err != nil {
		return err
	}

	// Reconcile VRF
	shouldReattachL2VNI, err := n.isL2VNIreattachRequired(current, desired)
	if err != nil {
		return err
	}

	if shouldReattachL2VNI {
		if err := n.reattachL2VNI(current); err != nil {
			return err
		}
	}

	if err := n.configureBridge(bridgeName); err != nil {
		return err
	}

	if err := n.doNeighSuppression(current, desired); err != nil {
		return err
	}

	currentGateways, err := n.ParseIPAddresses(current.AnycastGateways)
	if err != nil {
		return err
	}
	desiredGateways, err := n.ParseIPAddresses(desired.AnycastGateways)
	if err != nil {
		return err
	}
	// Add/Remove anycast gateways
	if err := n.reconcileIPAddresses(current.bridge, currentGateways, desiredGateways); err != nil {
		return err
	}

	// Reconcile EUI Autogeneration
	return n.reconcileEUIAutogeneration(bridgeName, current.bridge, desiredGateways)
}

func (n *Manager) setMTU(current, desired *Layer2Information) error {
	// Set MTU
	if err := n.toolkit.LinkSetMTU(current.bridge, desired.MTU); err != nil {
		return fmt.Errorf("error setting bridge MTU: %w", err)
	}
	if err := n.toolkit.LinkSetMTU(current.vxlan, desired.MTU); err != nil {
		return fmt.Errorf("error setting vxlan MTU: %w", err)
	}
	if err := n.toolkit.LinkSetMTU(current.vlanInterface, desired.MTU); err != nil {
		return fmt.Errorf("error setting vlan interface MTU: %w", err)
	}
	return nil
}

func (n *Manager) setHardwareAddresses(current, desired *Layer2Information, vxlanMAC net.HardwareAddr) error {
	if desired.AnycastMAC != nil && current.bridge.HardwareAddr.String() != *desired.AnycastMAC {
		if err := n.toolkit.LinkSetDown(current.vxlan); err != nil {
			return fmt.Errorf("error downing vxlan before changing MAC: %w", err)
		}
		time.Sleep(interfaceConfigTimeout) // Wait for FRR to pickup interface down

		if desired.AnycastMAC != nil {
			mac, err := net.ParseMAC(*desired.AnycastMAC)
			if err != nil {
				return fmt.Errorf("error while parsing MAC address: %w", err)
			}
			if err := n.toolkit.LinkSetHardwareAddr(current.bridge, mac); err != nil {
				return fmt.Errorf("error setting bridge mac address: %w", err)
			}
		}

		time.Sleep(interfaceConfigTimeout)
		if err := n.toolkit.LinkSetUp(current.vxlan); err != nil {
			return fmt.Errorf("error upping vxlan after changing MAC: %w", err)
		}
	}
	if !bytes.Equal(current.vxlan.HardwareAddr, vxlanMAC) {
		if err := n.toolkit.LinkSetHardwareAddr(current.vxlan, vxlanMAC); err != nil {
			return fmt.Errorf("error setting vxlan mac address: %w", err)
		}
	}
	return nil
}

func (n *Manager) reattachL2VNI(current *Layer2Information) error {
	// First set VXLAN down and detach from L2VNI bridge
	if err := n.toolkit.LinkSetDown(current.vxlan); err != nil {
		return fmt.Errorf("error downing vxlan before changing MAC: %w", err)
	}
	if err := n.toolkit.LinkSetNoMaster(current.vxlan); err != nil {
		return fmt.Errorf("error removing vxlan from bridge before changing MAC: %w", err)
	}

	// Reattach VXLAN to L2VNI bridge
	if err := n.toolkit.LinkSetMaster(current.vxlan, current.bridge); err != nil {
		return fmt.Errorf("error adding vxlan to bridge after changing MAC: %w", err)
	}

	// Disable learning on bridgeport
	if err := n.toolkit.LinkSetLearning(current.vxlan, false); err != nil {
		return fmt.Errorf("error setting vxlan learning to false: %w", err)
	}

	// Up VXLAN interface again
	if err := n.toolkit.LinkSetUp(current.vxlan); err != nil {
		return fmt.Errorf("error uping vxlan after changing MAC: %w", err)
	}
	return nil
}

func (n *Manager) doNeighSuppression(current, desired *Layer2Information) error {
	neighSuppression := os.Getenv("NWOP_NEIGH_SUPPRESSION") == "true"
	if len(desired.AnycastGateways) == 0 {
		neighSuppression = false
	}
	if desired.NeighSuppression != nil {
		neighSuppression = *desired.NeighSuppression
	}
	return n.setNeighSuppression(current.vxlan, neighSuppression)
}

func (n *Manager) isL2VNIreattachRequired(current, desired *Layer2Information) (bool, error) {
	shouldReattachL2VNI := false
	// Reconcile VRF
	if current.VRF != desired.VRF {
		shouldReattachL2VNI = true
		if desired.VRF != "" {
			vrfID, err := n.GetVRFInterfaceIdxByName(desired.VRF)
			if err != nil {
				return shouldReattachL2VNI, fmt.Errorf("error while getting L3 by name: %w", err)
			}
			if err := n.toolkit.LinkSetMasterByIndex(current.bridge, vrfID); err != nil {
				return shouldReattachL2VNI, fmt.Errorf("error while setting master by index: %w", err)
			}
		} else {
			if err := n.toolkit.LinkSetNoMaster(current.bridge); err != nil {
				return shouldReattachL2VNI, fmt.Errorf("error while trying to link set no master: %w", err)
			}
		}
	}

	protinfo, err := n.toolkit.LinkGetProtinfo(current.vxlan)
	if err != nil {
		return shouldReattachL2VNI, fmt.Errorf("error getting bridge prot info: %w", err)
	}
	if protinfo.Learning {
		shouldReattachL2VNI = true
	}

	return shouldReattachL2VNI, nil
}

func (*Manager) configureBridge(intfName string) error {
	// Ensure bridge can receive gratitious ARP
	if err := os.WriteFile(fmt.Sprintf("%s/ipv4/conf/%s/arp_accept", procSysNetPath, intfName), []byte("1"), neighFilePermissions); err != nil {
		return fmt.Errorf("error setting arp_accept = 1 for interface: %w", err)
	}

	// Ensure we can receive unsolicited and solicited but untracked NA
	if _, err := os.Stat(fmt.Sprintf("%s/ipv6/conf/%s/accept_untracked_na", procSysNetPath, intfName)); err == nil {
		if err := os.WriteFile(fmt.Sprintf("%s/ipv6/conf/%s/accept_untracked_na", procSysNetPath, intfName), []byte("2"), neighFilePermissions); err != nil {
			return fmt.Errorf("error setting accept_untracked_na = 2 for interface: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("error checking if accept_untracked_na exists: %w", err)
	}

	baseTimer := os.Getenv("NWOP_NEIGH_BASE_REACHABLE_TIME")
	if baseTimer == "" {
		baseTimer = "30000"
	}

	// Ensure Ipv4 Neighbor expiry is set to 30min
	if err := os.WriteFile(fmt.Sprintf("%s/ipv4/neigh/%s/base_reachable_time_ms", procSysNetPath, intfName), []byte(baseTimer), neighFilePermissions); err != nil {
		return fmt.Errorf("error setting ipv4 base_reachable_time_ms = %s for interface: %w", baseTimer, err)
	}
	// Ensure IPv6 Neighbor expiry is set to 30min
	if err := os.WriteFile(fmt.Sprintf("%s/ipv6/neigh/%s/base_reachable_time_ms", procSysNetPath, intfName), []byte(baseTimer), neighFilePermissions); err != nil {
		return fmt.Errorf("error setting ipv6 base_reachable_time_ms = %s for interface: %w", baseTimer, err)
	}
	return nil
}

func (n *Manager) ListNeighborInformation() ([]NeighborInformation, error) {
	netlinkNeighbors, err := n.listNeighbors()
	if err != nil {
		return nil, err
	}
	fdbTable, err := n.listBridgeForwardingTable()
	if err != nil {
		return nil, err
	}
	neighborLinks, err := n.ListNeighborInterfaces()
	if err != nil {
		return nil, err
	}
	netlinkNeighbors = append(netlinkNeighbors, fdbTable...)
	neighbors := map[NeighborKey]NeighborInformation{}
	for index := range netlinkNeighbors {
		linkInfo, ok := neighborLinks[netlinkNeighbors[index].LinkIndex]
		if !ok {
			// we don't care if a link is not available
			// as it could be removed between our LinkByIndex and arp lookup
			continue
		}
		interfaceName := linkInfo.Attrs().Name
		// This ensures that only neighbors of secondary interfaces are imported
		// or hardware interfaces which support VFs

		neighborKey := NeighborKey{InterfaceIndex: netlinkNeighbors[index].LinkIndex, State: netlinkNeighbors[index].State, Flags: netlinkNeighbors[index].Flags, Family: netlinkNeighbors[index].Family}
		neighborInformation, ok := neighbors[neighborKey]
		if ok {
			neighborInformation.Quantity++
			neighbors[neighborKey] = neighborInformation
		} else {
			family, err := GetAddressFamily(netlinkNeighbors[index].Family)
			if err != nil {
				return nil, fmt.Errorf("error converting addressFamily: %w", err)
			}
			state, err := getNeighborState(netlinkNeighbors[index].State)
			if err != nil {
				return nil, fmt.Errorf("error converting neighborState: %w", err)
			}
			flag, err := getFlags(netlinkNeighbors[index].Flags)
			if err != nil {
				return nil, fmt.Errorf("error converting flag: %w", err)
			}
			neighbors[neighborKey] = NeighborInformation{
				Family:    family,
				State:     state,
				Interface: interfaceName,
				Flag:      flag,
				Quantity:  1,
			}
		}
	}

	return maps.Values(neighbors), nil
}
