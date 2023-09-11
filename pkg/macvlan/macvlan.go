package macvlan

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

const checkInterval = 5 * time.Second

var trackedBridges []int

func checkTrackedInterfaces(logger logr.Logger) {
	for _, intfIdx := range trackedBridges {
		intf, err := netlink.LinkByIndex(intfIdx)
		if err != nil {
			logger.Error(err, "couldn't load interface idx %d", intfIdx)
		}

		syncInterface(intf.(*netlink.Bridge), logger)
	}
}

func ensureMACDummyIntf(intf *netlink.Bridge, logger logr.Logger) (netlink.Link, error) {
	name := fmt.Sprintf("mvd.%s", intf.Attrs().Name)
	macDummy, err := netlink.LinkByName(name)
	if err != nil {
		var linkNotFoundErr *netlink.LinkNotFoundError
		if !errors.As(err, &linkNotFoundErr) {
			return nil, fmt.Errorf("error getting link by name: %w", err)
		}

		macDummy = &netlink.Dummy{
			LinkAttrs: netlink.NewLinkAttrs(),
		}
		macDummy.Attrs().Name = name
		macDummy.Attrs().MasterIndex = intf.Attrs().Index
		macDummy.Attrs().MTU = intf.Attrs().MTU
		err = netlink.LinkAdd(macDummy)
		if err != nil {
			return nil, fmt.Errorf("error adding link %s: %w", macDummy.Attrs().Name, err)
		}
		err = netlink.LinkSetDown(macDummy)
		if err != nil {
			return nil, fmt.Errorf("error setting link %s down: %w", macDummy.Attrs().Name, err)
		}
	}
	if macDummy.Attrs().OperState != netlink.OperDown {
		logger.Info("interface %s not down, setting down - otherwise it would route traffic", name)
		err = netlink.LinkSetDown(macDummy)
		if err != nil {
			return nil, fmt.Errorf("error setting link %s down: %w", macDummy.Attrs().Name, err)
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

func syncInterface(intf *netlink.Bridge, logger logr.Logger) {
	// First ensure that we have a dummy interface
	dummy, err := ensureMACDummyIntf(intf, logger)
	if err != nil {
		logger.Error(err, "error syncing interface %s", intf.Attrs().Name)
		return
	}

	configureNeighbors(intf, dummy, logger)
}

func configureNeighbors(intf *netlink.Bridge, dummy netlink.Link, logger logr.Logger) {
	// Get neighbors of bridge
	bridgeNeighbors, err := netlink.NeighList(intf.Attrs().Index, unix.AF_BRIDGE)
	if err != nil {
		logger.Error(err, "error syncing interface %s", intf.Attrs().Name)
		return
	}
	requiredMACAddresses := []net.HardwareAddr{}
	for i := range bridgeNeighbors {
		neigh := &bridgeNeighbors[i]
		// Look for unicast neighbor entries like "02:03:04:05:06:07 dev <bridge> self permanent"
		if neigh.MasterIndex == 0 && neigh.Flags == netlink.NTF_SELF && neigh.State == netlink.NUD_PERMANENT && isUnicastMac(neigh.HardwareAddr) {
			requiredMACAddresses = append(requiredMACAddresses, neigh.HardwareAddr)
		}
	}

	// Get neighbors of dummy
	dummyNeighbors, err := netlink.NeighList(dummy.Attrs().Index, unix.AF_BRIDGE)
	if err != nil {
		logger.Error(err, "error syncing interface %s", intf.Attrs().Name)
		return
	}

	alreadyExisting := getAlreadyExistingNeighbors(dummyNeighbors, requiredMACAddresses, dummy.Attrs().Name, intf.Attrs().Name, logger)

	// Add required MAC addresses when they are not yet existing (aka in alreadyExisting slice)
	for _, neigh := range requiredMACAddresses {
		if !containsMACAddress(alreadyExisting, neigh) {
			logger.Info("adding MAC address %s on dummy interface %s of bridge %s", neigh, dummy.Attrs().Name, intf.Attrs().Name)
			err = netlink.NeighSet(createNeighborEntry(neigh, dummy.Attrs().Index, intf.Attrs().Index))
			if err != nil {
				logger.Error(err, "error adding neighbor %s to intf %s (br %s)", neigh, dummy.Attrs().Name, intf.Attrs().Name)
			}
		}
	}
}

func getAlreadyExistingNeighbors(dummyNeighbors []netlink.Neigh, requiredMACAddresses []net.HardwareAddr, dummyName, intfName string, logger logr.Logger) []net.HardwareAddr {
	alreadyExisting := []net.HardwareAddr{}
	for i := range dummyNeighbors {
		neigh := &dummyNeighbors[i]
		// Look for unicast neighbor entries with no flags, no vlan, NUD_NOARP (static) fdb entries
		if neigh.Vlan == 0 && neigh.Flags == 0 && neigh.State == netlink.NUD_NOARP && isUnicastMac(neigh.HardwareAddr) {
			if !containsMACAddress(requiredMACAddresses, neigh.HardwareAddr) {
				// If MAC Address is not in required MAC addresses, delete neighbor
				if err := netlink.NeighDel(neigh); err != nil {
					logger.Error(err, "error deleting neighbor %v", neigh)
				}
				logger.Info("removed MAC address %s from dummy interface %s of bridge %s", neigh.HardwareAddr, dummyName, intfName)
			} else {
				// Add MAC address to alreadyExisting table
				alreadyExisting = append(alreadyExisting, neigh.HardwareAddr)
			}
		}
	}
	return alreadyExisting
}

func RunMACSync(interfacePrefix string) {
	logger := ctrl.Log.WithName("macvlan")
	links, err := netlink.LinkList()
	if err != nil {
		logger.Error(err, "couldn't load interfaces")
		return
	}
	for _, link := range links {
		if strings.HasPrefix(link.Attrs().Name, interfacePrefix) && link.Type() == "bridge" {
			logger.Info("tracking interface %s (bridge and Prefix '%s')", link.Attrs().Name, interfacePrefix)
			trackedBridges = append(trackedBridges, link.Attrs().Index)
		}
	}

	if len(trackedBridges) > 0 {
		go func() {
			for {
				checkTrackedInterfaces(logger)
				time.Sleep(checkInterval)
			}
		}()
	}
}
