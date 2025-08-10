package neighborsync

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cilium/ebpf/ringbuf"
	"github.com/mdlayher/arp"
	"github.com/mdlayher/ndp"
	"github.com/telekom/das-schiff-network-operator/pkg/bpf"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

const HardwareAddrLen = 6

var (
	refreshEvery = time.Second * 10
	neighbors    = make(map[timerKey]*timer)
	neighborsMu  sync.Mutex

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
	neighborsMu.Lock()
	defer neighborsMu.Unlock()
	if t, exists := neighbors[key]; !exists {
		neighbors[key] = &timer{NextRun: time.Now().Add(refreshEvery), Address: destination}
	} else {
		t.Address = destination
	}
}

func createTimerIfNotExistsForNeigh(addr netip.Addr, neigh *netlink.Neigh) {
	createTimerIfNotExists(neigh.LinkIndex, neigh.HardwareAddr, addr)
}

func deleteTimerIfExists(linkIndex int, address netip.Addr) {
	key := timerKey{LinkIndex: linkIndex, Address: address}
	neighborsMu.Lock()
	delete(neighbors, key)
	neighborsMu.Unlock()
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

func sendGratuitousNeighbor(linkIndex int, address netip.Addr, mac net.HardwareAddr) {
	intf, err := net.InterfaceByIndex(linkIndex)
	if err != nil {
		ctrl.Log.Error(err, "failed to get interface by index", "index", linkIndex)
		return
	}

	switch {
	case address.Is4():
		sendGratuitousARP(intf, address, mac)
	case address.Is6():
		sendUnsolicitedNA(intf, address, mac)
	default:
		ctrl.Log.Error(fmt.Errorf("unsupported IP address type: %s", address), "sendGratuitousNeighbor failed")
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

	c, _, err := ndp.Listen(iface, ndp.LinkLocal)
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

	if err := c.WriteTo(m, nil, address); err != nil {
		ctrl.Log.Error(err, "sendNDPRequest failed", "address", address)
	}
}

// sendGratuitousARP emits a broadcast ARP reply announcing ip->mac.
func sendGratuitousARP(ifi *net.Interface, nip netip.Addr, mac net.HardwareAddr) {
	c, err := arp.Dial(ifi)
	if err != nil {
		ctrl.Log.Error(err, "failed to dial ARP", "interface", ifi.Name)
		return
	}
	defer c.Close()

	// ARP reply with sender and target set to the same mapping.
	pkt, err := arp.NewPacket(arp.OperationReply, mac, nip, mac, nip)
	if err != nil {
		ctrl.Log.Error(err, "failed to create ARP packet", "interface", ifi.Name, "address", nip)
		return
	}
	bcast := net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if err := c.WriteTo(pkt, bcast); err != nil {
		ctrl.Log.Error(err, "sendGratuitousARP failed", "interface", ifi.Name, "address", nip)
	}
}

// sendUnsolicitedNA emits an unsolicited neighbor advertisement to all-nodes multicast.
func sendUnsolicitedNA(ifi *net.Interface, nip netip.Addr, mac net.HardwareAddr) {
	// Build NA message: Override=1, Solicited=0, Router=0, include TLLA option.
	na := &ndp.NeighborAdvertisement{
		Router:        false,
		Solicited:     false,
		Override:      true,
		TargetAddress: nip,
		Options: []ndp.Option{&ndp.LinkLayerAddress{
			Direction: ndp.Target,
			Addr:      mac,
		}},
	}
	// Open an NDP connection bound to ifi and send to ff02::1 (all-nodes)
	conn, _, err := ndp.Listen(ifi, ndp.LinkLocal)
	if err != nil {
		ctrl.Log.Error(err, "failed to open socket for NDP messages", "interface", ifi.Name)
		return
	}
	defer conn.Close()
	dst, _ := netip.ParseAddr("ff02::1")
	if err := conn.WriteTo(na, nil, dst); err != nil {
		ctrl.Log.Error(err, "sendUnsolicitedNA failed", "interface", ifi.Name, "address", nip)
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
		ipBytes := ipNet.IP.To4()
		if ipBytes != nil {
			ip, ok := netip.AddrFromSlice(ipBytes)
			if ok && ip.Is4() {
				return ip, nil
			}
		}
	}
	return netip.Addr{}, fmt.Errorf("no valid IPv4 address found on interface %s", iface.Name)
}

func processUpdate(update *netlink.NeighUpdate) {
	logger := ctrl.Log.WithName("neighborsync")

	intf, err := net.InterfaceByIndex(update.Neigh.LinkIndex)
	if err != nil {
		return
	}

	if !shouldTrackInterface(intf.Name) {
		return
	}

	addr, ok := netip.AddrFromSlice(update.Neigh.IP)
	if !ok {
		return
	}

	switch update.Type {
	case unix.RTM_NEWNEIGH:
		logger.Info("Received neighbor update", "link", intf.Name, "ip", update.Neigh.IP, "hardwareAddr", update.Neigh.HardwareAddr, "state", update.Neigh.State, "flags", update.Neigh.Flags)
		handleNeighborAdd(addr, &update.Neigh)
	case unix.RTM_DELNEIGH:
		logger.Info("Received neighbor delete", "link", intf.Name, "ip", update.Neigh.IP, "hardwareAddr", update.Neigh.HardwareAddr, "state", update.Neigh.State, "flags", update.Neigh.Flags)
		handleNeighborDelete(addr, &update.Neigh)
	}
}

func handleNeighborAdd(addr netip.Addr, neigh *netlink.Neigh) {
	if neigh.State&netlink.NUD_PERMANENT != 0 || neigh.Flags&netlink.NTF_EXT_LEARNED != 0 {
		// Send gratuitous ARP/NA when moving to permanent and extern_learned
		if neigh.State&netlink.NUD_PERMANENT != 0 && neigh.Flags&netlink.NTF_EXT_LEARNED != 0 {
			sendGratuitousNeighbor(neigh.LinkIndex, addr, neigh.HardwareAddr)
		}

		// When the neighbor is moving to permanent or learned, we should stop tracking it.
		deleteTimerIfExists(neigh.LinkIndex, addr)
		return
	}

	if neigh.State&netlink.NUD_REACHABLE != 0 {
		createTimerIfNotExistsForNeigh(addr, neigh)
	}

	if neigh.State&netlink.NUD_STALE != 0 {
		sendNeighborRequest(neigh.LinkIndex, neigh.HardwareAddr, addr)
	}
}

func handleNeighborDelete(addr netip.Addr, neigh *netlink.Neigh) {
	deleteTimerIfExists(neigh.LinkIndex, addr)
}

func shouldTrackInterface(name string) bool {
	for _, prefix := range l2InterfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func receiveUpdates(toolkit nl.ToolkitInterface) {
	logger := ctrl.Log.WithName("neighborsync")

	for {
		updates := make(chan netlink.NeighUpdate)
		done := make(chan struct{})
		err := toolkit.NeighSubscribeWithOptions(updates, done, netlink.NeighSubscribeOptions{ListExisting: true})
		if err != nil {
			logger.Error(err, "failed to subscribe to neighbor updates")
			break
		}
		for update := range updates {
			processUpdate(&update)
		}
		close(done)
		logger.Info("neighbor updates channel closed, restarting neighbor sync, clearing timers")
		neighborsMu.Lock()
		neighbors = make(map[timerKey]*timer)
		neighborsMu.Unlock()
		time.Sleep(time.Second)
	}
}

func runNeighborCheck() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		var interfaceRemoved []timerKey

		neighborsMu.Lock()
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
		neighborsMu.Unlock()
	}
}

func runBpfNeighborSync(toolkit nl.ToolkitInterface) {
	// Open ring buffer reader.
	rd, err := ringbuf.NewReader(bpf.EbpfNeighborRingbuf())
	if err != nil {
		ctrl.Log.Error(err, "failed to open ringbuf reader")
	}
	defer rd.Close()

	for {
		rec, err := rd.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return
			}
			// EAGAIN is okay
			var errno syscall.Errno
			if errors.As(err, &errno) && (errno == unix.EINTR || errno == unix.EAGAIN) {
				continue
			}
			ctrl.Log.Error(err, "failed to read ringbuf")
			return
		}

		// Parse loader.NeighborEvent from rec.RawSample
		var ev bpf.NeighborEvent
		if len(rec.RawSample) < bpf.NeighborEventSize { // 4+1+6+16
			continue
		}
		// Use unsafe-free manual copy
		// Fields: ifindex u32, family u8, mac[6], ip[16]
		b := rec.RawSample
		ev.Ifindex = binary.LittleEndian.Uint32(b[0:4])
		ev.Family = bpf.AddressFamily(b[4])
		copy(ev.Mac[:], b[5:11])
		copy(ev.IP[:], b[11:27])

		ctrl.Log.Info(fmt.Sprintf("ifindex=%d family=%d ip=%s mac=%s\n", ev.Ifindex, ev.Family, net.IP(ev.IP[:]), net.IP(ev.Mac[:])))

		// Update neighbor table to REACHABLE for this mapping.
		if err := replaceNeighborReachable(toolkit, int(ev.Ifindex), ev.Family, ev.IP, ev.Mac); err != nil {
			ctrl.Log.Error(err, "neigh replace failed")
		}
	}
}

// replaceNeighborReachable sets/updates a neighbor entry for the given mapping with state REACHABLE.
func replaceNeighborReachable(toolkit nl.ToolkitInterface, ifindex int, family bpf.AddressFamily, ipbuf [16]byte, macbuf [6]byte) error {
	var ip net.IP
	var fam int
	if family == bpf.AddressFamilyIPv4 {
		ip = net.IP(ipbuf[:4])
		fam = unix.AF_INET
	} else {
		ip = net.IP(ipbuf[:])
		fam = unix.AF_INET6
	}
	if len(ip) == 0 {
		return fmt.Errorf("empty IP from event")
	}
	hw := net.HardwareAddr(macbuf[:])
	if len(hw) != HardwareAddrLen {
		return fmt.Errorf("invalid MAC from event")
	}

	n := &netlink.Neigh{
		LinkIndex:    ifindex,
		Family:       fam,
		State:        netlink.NUD_REACHABLE,
		IP:           ip,
		HardwareAddr: hw,
	}
	if err := toolkit.NeighSet(n); err != nil {
		return fmt.Errorf("failed to set neighbor: %w", err)
	}
	return nil
}

// StartNeighborSync starts the neighbor synchronization process.
// a) Each netlink (non-extern_learn, non-permanent) neighbor entries are checked regularly. This is accomplished by periodically sending ARP requests for IPv4 and Neighbor Solicitation messages for IPv6.
// b) BPF is attached to the "l2v." interfaces, reading all ARP responses and Neighbor Advertisements. When a neighbor entry is detected, it is added to the neighbor table as reachable.
// c) When a neighbor moves to extern_learned or permanent state, it is no longer refreshed **by this instance**. However a gratuitous ARP request or Neighbor Solicitation is generated to notify local apps.
// We don't make use of the extended community defined in RFC 9047, however there shouldn't be any RAs / router flags on the vlans we watch here (we are the router!)
func StartNeighborSync(toolkit nl.ToolkitInterface) {
	go receiveUpdates(toolkit)

	go runNeighborCheck()

	go runBpfNeighborSync(toolkit)
}
