package macvlan

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

var trackedBridges []int

func checkTrackedInterfaces() {
	for _, intfIdx := range trackedBridges {
		intf, err := netlink.LinkByIndex(intfIdx)
		if err != nil {
			fmt.Printf("Couldn't load interface idx %d: %v\n", intfIdx, err)
		}

		syncInterface(intf.(*netlink.Bridge))
	}
}

func ensureMACDummyIntf(intf *netlink.Bridge) (netlink.Link, error) {
	name := fmt.Sprintf("mvd.%s", intf.Attrs().Name)
	macDummy, err := netlink.LinkByName(name)
	if err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); ok {
			macDummy = &netlink.Dummy{
				LinkAttrs: netlink.NewLinkAttrs(),
			}
			macDummy.Attrs().Name = name
			macDummy.Attrs().MasterIndex = intf.Attrs().Index
			macDummy.Attrs().MTU = intf.Attrs().MTU
			err = netlink.LinkAdd(macDummy)
			if err != nil {
				return nil, err
			}
			err = netlink.LinkSetDown(macDummy)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	if macDummy.Attrs().OperState != netlink.OperDown {
		fmt.Printf("Interface %s not down, setting down - otherwise it would route traffic\n", name)
		err = netlink.LinkSetDown(macDummy)
		if err != nil {
			return nil, err
		}
	}
	return macDummy, nil
}

func createNeighborEntry(mac net.HardwareAddr, intf, master int) *netlink.Neigh {
	return &netlink.Neigh{
		State:        netlink.NUD_NOARP,
		Family:       unix.AF_BRIDGE,
		HardwareAddr: mac,
		LinkIndex:    intf,
		MasterIndex:  master,
	}
}

func isUnicastMac(mac net.HardwareAddr) bool {
	return mac[0]&0x01 == 0
}

func containsMACAddress(list []net.HardwareAddr, mac net.HardwareAddr) bool {
	for _, v := range list {
		if bytes.Equal(v, mac) {
			return true
		}
	}
	return false
}

func syncInterface(intf *netlink.Bridge) {
	// First ensure that we have a dummy interface
	dummy, err := ensureMACDummyIntf(intf)
	if err != nil {
		fmt.Printf("Error syncing interface %s: %v\n", intf.Attrs().Name, err)
		return
	}

	// Get neighbors of bridge
	bridgeNeighbors, err := netlink.NeighList(intf.Attrs().Index, unix.AF_BRIDGE)
	if err != nil {
		fmt.Printf("Error syncing interface %s: %v\n", intf.Attrs().Name, err)
		return
	}
	requiredMACAddresses := []net.HardwareAddr{}
	for _, neigh := range bridgeNeighbors {
		// Look for unicast neighbor entries like "02:03:04:05:06:07 dev <bridge> self permanent"
		if neigh.MasterIndex == 0 && neigh.Flags == netlink.NTF_SELF && neigh.State == netlink.NUD_PERMANENT && isUnicastMac(neigh.HardwareAddr) {
			requiredMACAddresses = append(requiredMACAddresses, neigh.HardwareAddr)
		}
	}

	// Get neighbors of dummy
	dummyNeighbors, err := netlink.NeighList(dummy.Attrs().Index, unix.AF_BRIDGE)
	if err != nil {
		fmt.Printf("Error syncing interface %s: %v\n", intf.Attrs().Name, err)
		return
	}
	alreadyExisting := []net.HardwareAddr{}
	for _, neigh := range dummyNeighbors {
		// Look for unicast neighbor entries with no flags, no vlan, NUD_NOARP (static) fdb entries
		if neigh.Vlan == 0 && neigh.Flags == 0 && neigh.State == netlink.NUD_NOARP && isUnicastMac(neigh.HardwareAddr) {
			if !containsMACAddress(requiredMACAddresses, neigh.HardwareAddr) {
				// If MAC Address is not in required MAC addresses, delete neighbor
				err = netlink.NeighDel(&neigh)
				if err != nil {
					fmt.Printf("Error deleting neighbor %v: %v\n", neigh, err)
				}
				fmt.Printf("Removed MAC address %s from dummy interface %s of bridge %s\n", neigh.HardwareAddr, dummy.Attrs().Name, intf.Attrs().Name)
			} else {
				// Add MAC address to alreadyExisting table
				alreadyExisting = append(alreadyExisting, neigh.HardwareAddr)
			}
		}
	}

	// Add required MAC addresses when they are not yet existing (aka in alreadyExisting slice)
	for _, neigh := range requiredMACAddresses {
		if !containsMACAddress(alreadyExisting, neigh) {
			fmt.Printf("Adding MAC address %s on dummy interface %s of bridge %s\n", neigh, dummy.Attrs().Name, intf.Attrs().Name)
			err = netlink.NeighSet(createNeighborEntry(neigh, dummy.Attrs().Index, intf.Attrs().Index))
			if err != nil {
				fmt.Printf("Error adding neighbor %s to intf %s (br %s): %v\n", neigh, dummy.Attrs().Name, intf.Attrs().Name, err)
			}
		}
	}
}

func RunMACSync(interfacePrefix string) {
	links, err := netlink.LinkList()
	if err != nil {
		fmt.Printf("Couldn't load interfaces: %v\n", err)
		return
	}
	for _, link := range links {
		if strings.HasPrefix(link.Attrs().Name, interfacePrefix) && link.Type() == "bridge" {
			fmt.Printf("Tracking interface %s (bridge and Prefix '%s')\n", link.Attrs().Name, interfacePrefix)
			trackedBridges = append(trackedBridges, link.Attrs().Index)
		}
	}

	if len(trackedBridges) > 0 {
		go func() {
			for {
				checkTrackedInterfaces()
				time.Sleep(5 * time.Second)
			}
		}()
	}
}
