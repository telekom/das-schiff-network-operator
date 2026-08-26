package macvlan

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

const checkInterval = 1 * time.Second

// trackedVLAN maps a vlan.XXXX interface to its corresponding bridge port.
type trackedVLAN struct {
	vlanName       string
	vlanIdx        int
	bridgePortName string
	bridgePortIdx  int
	bridgeName     string
	bridgeIdx      int
	previousMACs   map[string]struct{}
}

var trackedVLANs []*trackedVLAN

func isUnicastMac(mac net.HardwareAddr) bool {
	return len(mac) >= 1 && mac[0]&0x01 == 0
}

func containsMACAddress(list []net.HardwareAddr, mac net.HardwareAddr) bool {
	for _, v := range list {
		if bytes.Equal(v, mac) {
			return true
		}
	}
	return false
}

// readSelfPermanentMACs reads the FDB of a vlan.* interface and returns all
// unicast MACs with NTF_SELF flag (these are macvlan slave MACs).
func readSelfPermanentMACs(linkIdx int) map[string]struct{} {
	result := make(map[string]struct{})
	neighs, err := netlink.NeighList(linkIdx, unix.AF_BRIDGE)
	if err != nil {
		return result
	}
	for i := range neighs {
		n := &neighs[i]
		if n.MasterIndex == 0 && n.Flags&netlink.NTF_SELF != 0 && isUnicastMac(n.HardwareAddr) {
			result[n.HardwareAddr.String()] = struct{}{}
		}
	}
	return result
}

// pollVLANInterfaces checks each tracked vlan.* interface for disappeared
// MACs and removes stale bridge-learned FDB entries from the corresponding
// l2v.* bridge port. Two checks are performed:
//  1. Diff check: MACs that disappeared since last poll
//  2. Consistency check: bridge-port entries without matching self-permanent
func pollVLANInterfaces(logger logr.Logger) {
	for _, tv := range trackedVLANs {
		currentMACs := readSelfPermanentMACs(tv.vlanIdx)

		// Diff check: find MACs that disappeared since last poll.
		for macStr := range tv.previousMACs {
			if _, exists := currentMACs[macStr]; !exists {
				deleteBridgePortFDB(tv, macStr, "disappeared from "+tv.vlanName, logger)
			}
		}

		// Consistency check: remove bridge-port entries without a
		// matching self-permanent on the vlan interface.
		cleanStaleBridgePortEntries(tv, currentMACs, logger)

		tv.previousMACs = currentMACs
	}
}

// cleanStaleBridgePortEntries reads the bridge-port FDB and deletes any
// unicast entry that has no corresponding self-permanent MAC on the vlan
// interface.
func cleanStaleBridgePortEntries(tv *trackedVLAN, validMACs map[string]struct{}, logger logr.Logger) {
	neighs, err := netlink.NeighList(tv.bridgePortIdx, unix.AF_BRIDGE)
	if err != nil {
		return
	}
	for i := range neighs {
		n := &neighs[i]
		if n.MasterIndex == 0 || !isUnicastMac(n.HardwareAddr) {
			continue
		}
		if n.Flags&netlink.NTF_SELF != 0 {
			continue
		}
		macStr := n.HardwareAddr.String()
		if _, valid := validMACs[macStr]; !valid {
			deleteBridgePortFDB(tv, macStr, "stale on bridge port "+tv.bridgePortName, logger)
		}
	}
}

// deleteBridgePortFDB deletes a single MAC from a bridge port's FDB.
func deleteBridgePortFDB(tv *trackedVLAN, macStr, reason string, logger logr.Logger) {
	mac, err := net.ParseMAC(macStr)
	if err != nil {
		return
	}
	logger.Info("cleaning stale FDB entry", "mac", macStr, "reason", reason, "bridgePort", tv.bridgePortName)

	if err := netlink.NeighDel(&netlink.Neigh{
		LinkIndex:    tv.bridgePortIdx,
		Family:       unix.AF_BRIDGE,
		HardwareAddr: mac,
		MasterIndex:  tv.bridgeIdx,
	}); err != nil {
		logger.Error(err, "failed to delete stale FDB entry (may already be gone)",
			"mac", macStr, "bridgePort", tv.bridgePortName)
	} else {
		logger.Info("successfully removed stale FDB entry",
			"mac", macStr, "bridgePort", tv.bridgePortName)
	}
}

// RunMACSync starts the MAC synchronization loop. The interfacePrefix
// parameter selects which vlan.* interfaces to track (e.g. "vlan.").
// For each tracked vlan.XXXX, the corresponding l2v.XXXX bridge port and
// l2.XXXX bridge are discovered automatically.
func RunMACSync(interfacePrefix string) {
	logger := ctrl.Log.WithName("macvlan")
	links, err := netlink.LinkList()
	if err != nil {
		logger.Error(err, "error loading interfaces")
		return
	}

	// Index links by name for quick lookup.
	byName := make(map[string]netlink.Link, len(links))
	for _, l := range links {
		byName[l.Attrs().Name] = l
	}

	for _, link := range links {
		name := link.Attrs().Name
		if !strings.HasPrefix(name, interfacePrefix) {
			continue
		}

		// Extract suffix: vlan.1007 → 1007
		suffix := strings.TrimPrefix(name, interfacePrefix)

		// Find bridge port l2v.XXXX
		bpName := fmt.Sprintf("l2v.%s", suffix)
		bp, ok := byName[bpName]
		if !ok {
			logger.Info("no bridge port found, skipping", "vlan", name, "expected", bpName)
			continue
		}

		// Find bridge (master of bridge port)
		masterIdx := bp.Attrs().MasterIndex
		if masterIdx == 0 {
			logger.Info("bridge port has no master, skipping", "bridgePort", bpName)
			continue
		}
		master, err := netlink.LinkByIndex(masterIdx)
		if err != nil {
			logger.Error(err, "failed to get bridge for bridge port", "bridgePort", bpName)
			continue
		}

		tv := &trackedVLAN{
			vlanName:       name,
			vlanIdx:        link.Attrs().Index,
			bridgePortName: bpName,
			bridgePortIdx:  bp.Attrs().Index,
			bridgeName:     master.Attrs().Name,
			bridgeIdx:      masterIdx,
			previousMACs:   readSelfPermanentMACs(link.Attrs().Index),
		}
		trackedVLANs = append(trackedVLANs, tv)
		logger.Info("tracking vlan interface",
			"vlan", name, "vlanIdx", tv.vlanIdx,
			"bridgePort", bpName, "bridgePortIdx", tv.bridgePortIdx,
			"bridge", master.Attrs().Name, "bridgeIdx", masterIdx,
			"initialMACs", len(tv.previousMACs))
	}

	if len(trackedVLANs) > 0 {
		// Initial consistency cleanup: remove any pre-existing stale entries.
		for _, tv := range trackedVLANs {
			cleanStaleBridgePortEntries(tv, tv.previousMACs, logger)
		}
		logger.Info("starting FDB poll loop", "interval", checkInterval, "trackedInterfaces", len(trackedVLANs))
		go func() {
			for {
				time.Sleep(checkInterval)
				pollVLANInterfaces(logger)
			}
		}()
	} else {
		logger.Info("no vlan interfaces found matching prefix, MAC sync inactive", "prefix", interfacePrefix)
	}
}
