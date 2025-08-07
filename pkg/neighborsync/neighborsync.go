package neighborsync

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/mdlayher/arp"
	"github.com/mdlayher/ndp"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	refreshEvery = time.Second * 10
	neighbors    = make(map[timerKey]*timer)

	l2InterfacePrefixes = []string{"l2."}
)

type timerKey struct {
	LinkIndex int
	Address   netip.Addr
}

type timer struct {
	NextRun time.Time
	Address net.HardwareAddr
}

func createTimerIfNotExists(linkIndex int, destination net.HardwareAddr, address netip.Addr) {
	key := timerKey{LinkIndex: linkIndex, Address: address}
	if t, exists := neighbors[key]; !exists {
		neighbors[key] = &timer{NextRun: time.Now().Add(refreshEvery), Address: destination}
	} else {
		t.Address = destination
	}
}

func createTimerIfNotExistsForNeigh(neigh *netlink.Neigh) {
	addr, ok := netip.AddrFromSlice(neigh.IP)
	if ok {
		createTimerIfNotExists(neigh.LinkIndex, neigh.HardwareAddr, addr)
	}
}

func deleteTimerIfExists(linkIndex int, address netip.Addr) {
	key := timerKey{LinkIndex: linkIndex, Address: address}
	delete(neighbors, key)
}

func sendNeighborRequest(linkIndex int, destination net.HardwareAddr, address netip.Addr) {
	switch {
	case address.Is4():
		sendARPRequest(linkIndex, destination, address)
	case address.Is6():
		sendNDPRequest(linkIndex, destination, address)
	default:
		ctrl.Log.Error(fmt.Errorf("unsupported IP address type: %s", address), "sendNeighborRequest failed")
	}
}

func sendARPRequest(linkIndex int, destination net.HardwareAddr, address netip.Addr) {
	iface, err := net.InterfaceByIndex(linkIndex)
	if err != nil {
		return
	}
	c, err := arp.Dial(iface)
	if err != nil {
		return
	}
	defer c.Close()

	ip, err := getFirstIPv4FromInterface(iface)
	if err != nil {
		ctrl.Log.Error(err, "failed to get IPv4 address from interface", "interface", iface.Name)
		return
	}

	arpPacket, err := arp.NewPacket(arp.OperationRequest, iface.HardwareAddr, ip, destination, address)
	if err != nil {
		ctrl.Log.Error(err, "failed to create ARP packet", "interface", iface.Name, "address", address)
		return
	}

	if err := c.WriteTo(arpPacket, destination); err != nil {
		ctrl.Log.Error(err, "sendARPRequest failed", "interface", iface.Name, "address", address)
	}
}

func sendNDPRequest(linkIndex int, destination net.HardwareAddr, address netip.Addr) {
	iface, err := net.InterfaceByIndex(linkIndex)
	if err != nil {
		return
	}
	ip, err := getFirstNonLLIPv6FromInterface(iface)
	if err != nil {
		ctrl.Log.Error(err, "failed to get IPv6 address from interface", "interface", iface.Name)
		return
	}

	c, _, err := ndp.Listen(iface, ndp.Addr(ip.String()))
	if err != nil {
		ctrl.Log.Error(err, "failed to listen for NDP messages", "interface", iface.Name)
		return
	}
	defer c.Close()

	m := &ndp.NeighborSolicitation{
		TargetAddress: address,
		Options: []ndp.Option{
			&ndp.LinkLayerAddress{
				Direction: ndp.Source,
				Addr:      iface.HardwareAddr,
			},
			&ndp.LinkLayerAddress{
				Direction: ndp.Target,
				Addr:      destination,
			},
		},
	}

	if err := c.WriteTo(m, nil, ip); err != nil {
		ctrl.Log.Error(err, "sendNDPRequest failed", "address", address)
	}
}

func getFirstIPv4FromInterface(iface *net.Interface) (netip.Addr, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("failed to get addresses for interface %s: %w", iface.Name, err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipNet.IP)
		if ok && ip.Is4() {
			return ip, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no valid IPv4 address found on interface %s", iface.Name)
}

func getFirstNonLLIPv6FromInterface(iface *net.Interface) (netip.Addr, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("failed to get addresses for interface %s: %w", iface.Name, err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipNet.IP)
		if ok && ip.Is6() && !ip.IsLinkLocalUnicast() {
			return ip, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no valid global IPv6 address found on interface %s", iface.Name)
}

func processUpdate(update *netlink.NeighUpdate) {
	intf, err := net.InterfaceByIndex(update.Neigh.LinkIndex)
	if err != nil {
		ctrl.Log.Error(err, "failed to get interface by index", "index", update.Neigh.LinkIndex)
		return
	}

	if !shouldTrackInterface(intf.Name) {
		return
	}

	switch update.Type {
	case unix.RTM_NEWNEIGH:
		handleNeighborAdd(&update.Neigh)
	case unix.RTM_DELNEIGH:
		handleNeighborDelete(&update.Neigh)
	}
}

func handleNeighborAdd(neigh *netlink.Neigh) {
	if neigh.State&netlink.NUD_PERMANENT != 0 || neigh.Flags&netlink.NTF_EXT_LEARNED != 0 {
		return
	}

	if neigh.State&netlink.NUD_REACHABLE != 0 {
		createTimerIfNotExistsForNeigh(neigh)
	}

	if neigh.State&netlink.NUD_STALE != 0 {
		addr, ok := netip.AddrFromSlice(neigh.IP)
		if ok {
			sendNeighborRequest(neigh.LinkIndex, neigh.HardwareAddr, addr)
		}
	}
}

func handleNeighborDelete(neigh *netlink.Neigh) {
	addr, ok := netip.AddrFromSlice(neigh.IP)
	if ok {
		deleteTimerIfExists(neigh.LinkIndex, addr)
	}
}

func shouldTrackInterface(name string) bool {
	for _, prefix := range l2InterfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func loadAllNeighbors(toolkit nl.ToolkitInterface) error {
	links, err := toolkit.LinkList()
	if err != nil {
		return fmt.Errorf("failed to get link list: %w", err)
	}

	for i := range links {
		if !shouldTrackInterface(links[i].Attrs().Name) {
			continue
		}

		for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
			neighbors, err := toolkit.NeighList(links[i].Attrs().Index, family)
			if err != nil {
				return fmt.Errorf("failed to get neighbors for link %s: %w", links[i].Attrs().Name, err)
			}

			for j := range neighbors {
				handleNeighborAdd(&neighbors[j])
			}
		}
	}

	return nil
}

func receiveUpdates(toolkit nl.ToolkitInterface) {
	logger := ctrl.Log.WithName("neighborsync")

	for {
		if err := loadAllNeighbors(toolkit); err != nil {
			logger.Error(err, "failed to load all neighbors")
		}

		updates := make(chan netlink.NeighUpdate)
		done := make(chan struct{})
		err := toolkit.NeighSubscribe(updates, done)
		if err != nil {
			logger.Error(err, "failed to subscribe to neighbor updates")
			break
		}
		for update := range updates {
			processUpdate(&update)
		}
		close(done)
		logger.Info("neighbor updates channel closed, restarting neighbor sync, clearing timers")
		neighbors = make(map[timerKey]*timer)
		time.Sleep(time.Second)
	}
}

func runNeighborCheck() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		var interfaceRemoved []timerKey

		for key, timer := range neighbors {
			if time.Now().After(timer.NextRun) {
				if _, err := net.InterfaceByIndex(key.LinkIndex); err != nil {
					ctrl.Log.Info("interface removed, deleting neighbor timer", "linkIndex", key.LinkIndex, "address", key.Address)
					interfaceRemoved = append(interfaceRemoved, key)
					continue
				}

				sendNeighborRequest(key.LinkIndex, timer.Address, key.Address)
				timer.NextRun = time.Now().Add(refreshEvery)
			}
		}

		for _, key := range interfaceRemoved {
			delete(neighbors, key)
		}
	}
}

func StartNeighborSync(toolkit nl.ToolkitInterface) {
	go receiveUpdates(toolkit)

	go runNeighborCheck()
}
