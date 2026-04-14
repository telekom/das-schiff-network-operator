package neighborsync

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
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
)

const hardwareAddrLen = 6
const refreshEvery = time.Second * 10

type timerKey struct {
	LinkIndex int
	Address   netip.Addr
}

type timer struct {
	NextRun time.Time
	Address net.HardwareAddr
}

type NeighborSync struct {
	neighbors sync.Map // map[timerKey]*timer

	neighRefreshInterfaces sync.Map
	sendGratuitousNeighbor sync.Map
	receiveNeighbors       sync.Map

	nlOps nl.ToolkitInterface

	sendNeighborRequestFn    func(linkIndex int, destination net.HardwareAddr, address netip.Addr)
	sendGratuitousNeighborFn func(linkIndex int, address netip.Addr, mac net.HardwareAddr)
	// bpfAttachFn attaches the BPF program to an interface. Injectable for testing.
	bpfAttachFn func(link netlink.Link) error
	// bpfDetachFn detaches the BPF program from an interface. Injectable for testing.
	bpfDetachFn        func(link netlink.Link) error
	neighSubscribeFn   func(updates chan<- netlink.NeighUpdate, done <-chan struct{}, options netlink.NeighSubscribeOptions) error
	newRingbufReaderFn func() (*ringbuf.Reader, error)

	initOnce sync.Once
}

func (n *NeighborSync) createTimerIfNotExists(linkIndex int, destination net.HardwareAddr, address netip.Addr) {
	key := timerKey{LinkIndex: linkIndex, Address: address}
	actual, loaded := n.neighbors.LoadOrStore(key, &timer{NextRun: time.Now().Add(refreshEvery), Address: destination})
	if loaded {
		if t, ok := actual.(*timer); ok {
			t.Address = destination
		}
	}
}

func (n *NeighborSync) createTimerIfNotExistsForNeigh(addr netip.Addr, neigh *netlink.Neigh) {
	n.createTimerIfNotExists(neigh.LinkIndex, neigh.HardwareAddr, addr)
}

func (n *NeighborSync) deleteTimerIfExists(linkIndex int, address netip.Addr) {
	key := timerKey{LinkIndex: linkIndex, Address: address}
	n.neighbors.Delete(key)
}

func sendNeighborRequest(linkIndex int, destination net.HardwareAddr, address netip.Addr) {
	switch {
	case address.Is4():
		sendARPRequest(linkIndex, destination, address)
	case address.Is6():
		sendNDPRequest(linkIndex, destination, address)
	default:
		log.Printf("sendNeighborRequest: unsupported IP address type: %s", address)
	}
}

func sendGratuitousNeighbor(linkIndex int, address netip.Addr, mac net.HardwareAddr) {
	intf, err := net.InterfaceByIndex(linkIndex)
	if err != nil {
		log.Printf("failed to get interface by index %d: %v", linkIndex, err)
		return
	}

	switch {
	case address.Is4():
		sendGratuitousARP(intf, address, mac)
	case address.Is6():
		sendUnsolicitedNA(intf, address, mac)
	default:
		log.Printf("sendGratuitousNeighbor: unsupported IP address type: %s", address)
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
		log.Printf("failed to get IPv4 address from interface %s: %v", iface.Name, err)
		return
	}

	arpPacket, err := arp.NewPacket(arp.OperationRequest, iface.HardwareAddr, ip, destination, address)
	if err != nil {
		log.Printf("failed to create ARP packet on %s for %s: %v", iface.Name, address, err)
		return
	}

	if err := c.WriteTo(arpPacket, destination); err != nil {
		log.Printf("sendARPRequest failed on %s for %s: %v", iface.Name, address, err)
	}
}

func sendNDPRequest(linkIndex int, destination net.HardwareAddr, address netip.Addr) {
	iface, err := net.InterfaceByIndex(linkIndex)
	if err != nil {
		return
	}

	c, _, err := ndp.Listen(iface, ndp.LinkLocal)
	if err != nil {
		log.Printf("failed to listen for NDP on %s: %v", iface.Name, err)
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
		log.Printf("sendNDPRequest failed for %s: %v", address, err)
	}
}

// sendGratuitousARP emits a broadcast ARP reply announcing ip->mac.
func sendGratuitousARP(ifi *net.Interface, nip netip.Addr, mac net.HardwareAddr) {
	c, err := arp.Dial(ifi)
	if err != nil {
		log.Printf("failed to dial ARP on %s: %v", ifi.Name, err)
		return
	}
	defer c.Close()

	pkt, err := arp.NewPacket(arp.OperationReply, mac, nip, mac, nip)
	if err != nil {
		log.Printf("failed to create gratuitous ARP packet on %s for %s: %v", ifi.Name, nip, err)
		return
	}
	bcast := net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if err := c.WriteTo(pkt, bcast); err != nil {
		log.Printf("sendGratuitousARP failed on %s for %s: %v", ifi.Name, nip, err)
	}
}

// sendUnsolicitedNA emits an unsolicited neighbor advertisement to all-nodes multicast.
func sendUnsolicitedNA(ifi *net.Interface, nip netip.Addr, mac net.HardwareAddr) {
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
	conn, _, err := ndp.Listen(ifi, ndp.LinkLocal)
	if err != nil {
		log.Printf("failed to open NDP socket on %s: %v", ifi.Name, err)
		return
	}
	defer conn.Close()
	dst, _ := netip.ParseAddr("ff02::1")
	if err := conn.WriteTo(na, nil, dst); err != nil {
		log.Printf("sendUnsolicitedNA failed on %s for %s: %v", ifi.Name, nip, err)
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

func (n *NeighborSync) processUpdate(update *netlink.NeighUpdate) {
	if _, ok := n.neighRefreshInterfaces.Load(update.LinkIndex); !ok {
		return
	}

	addr, ok := netip.AddrFromSlice(update.IP)
	if !ok {
		return
	}

	switch update.Type {
	case unix.RTM_NEWNEIGH:
		log.Printf("neighbor update: link=%d ip=%s mac=%s state=%d flags=%d",
			update.LinkIndex, update.IP, update.HardwareAddr, update.State, update.Flags)
		n.handleNeighborAdd(addr, &update.Neigh)
	case unix.RTM_DELNEIGH:
		log.Printf("neighbor delete: link=%d ip=%s mac=%s state=%d flags=%d",
			update.LinkIndex, update.IP, update.HardwareAddr, update.State, update.Flags)
		n.handleNeighborDelete(addr, &update.Neigh)
	default:
		return
	}
}

func (n *NeighborSync) handleNeighborAdd(addr netip.Addr, neigh *netlink.Neigh) {
	if neigh.Flags&netlink.NTF_EXT_LEARNED != 0 {
		// Send gratuitous ARP/NA when creating an extern_learned
		if _, ok := n.sendGratuitousNeighbor.Load(neigh.LinkIndex); ok {
			n.sendGratuitousNeighborFn(neigh.LinkIndex, addr, neigh.HardwareAddr)
		}

		// When the neighbor is moving to extern_learned, also stop tracking it.
		n.deleteTimerIfExists(neigh.LinkIndex, addr)
		return
	}

	if neigh.State&netlink.NUD_REACHABLE != 0 {
		n.createTimerIfNotExistsForNeigh(addr, neigh)
	}

	if neigh.State&netlink.NUD_STALE != 0 {
		n.sendNeighborRequestFn(neigh.LinkIndex, neigh.HardwareAddr, addr)
	}
}

func (n *NeighborSync) handleNeighborDelete(addr netip.Addr, neigh *netlink.Neigh) {
	n.deleteTimerIfExists(neigh.LinkIndex, addr)
}

func (n *NeighborSync) receiveUpdates() {
	for {
		updates := make(chan netlink.NeighUpdate)
		done := make(chan struct{})
		err := n.neighSubscribeFn(updates, done, netlink.NeighSubscribeOptions{ListExisting: true})
		if err != nil {
			log.Printf("failed to subscribe to neighbor updates: %v", err)
			break
		}
		for update := range updates {
			n.processUpdate(&update)
		}
		close(done)
		log.Println("neighbor updates channel closed, restarting neighbor sync, clearing timers")
		n.neighbors = sync.Map{} // Clear all timers
		time.Sleep(time.Second)
	}
}

func (n *NeighborSync) syncKernelNeighbors(intfIndex int) {
	neighbors, err := n.nlOps.NeighList(intfIndex, netlink.FAMILY_ALL)
	if err != nil {
		log.Printf("failed to list neighbors: %v", err)
		return
	}

	for i := range neighbors {
		addr, ok := netip.AddrFromSlice(neighbors[i].IP)
		if !ok {
			continue
		}
		n.handleNeighborAdd(addr, &neighbors[i])
	}
}

func (n *NeighborSync) runNeighborCheck() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		var interfaceRemoved []timerKey

		n.neighbors.Range(func(key any, value any) bool {
			timerKeyVal, ok1 := key.(timerKey)
			timerVal, ok2 := value.(*timer)
			if !ok1 || !ok2 {
				return true
			}
			if time.Now().After(timerVal.NextRun) {
				if _, err := net.InterfaceByIndex(timerKeyVal.LinkIndex); err != nil {
					log.Printf("interface removed, deleting neighbor timer: linkIndex=%d address=%s",
						timerKeyVal.LinkIndex, timerKeyVal.Address)
					interfaceRemoved = append(interfaceRemoved, timerKeyVal)
					return true
				}

				n.sendNeighborRequestFn(timerKeyVal.LinkIndex, timerVal.Address, timerKeyVal.Address)
				timerVal.NextRun = time.Now().Add(refreshEvery)
			}
			return true
		})
		for _, key := range interfaceRemoved {
			n.neighbors.Delete(key)
		}
	}
}

func (n *NeighborSync) runBpfNeighborSync() {
	log.Println("BPF ringbuf reader goroutine started")
	rd, err := n.newRingbufReaderFn()
	if err != nil {
		log.Printf("failed to open ringbuf reader: %v", err)
		return
	}
	defer rd.Close()
	log.Println("BPF ringbuf reader opened, waiting for events...")

	for {
		rec, err := rd.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return
			}
			var errno syscall.Errno
			if errors.As(err, &errno) && (errno == unix.EINTR || errno == unix.EAGAIN) {
				continue
			}
			log.Printf("failed to read ringbuf: %v", err)
			return
		}

		var ev bpf.NeighborEvent
		if len(rec.RawSample) < bpf.NeighborEventSize {
			continue
		}
		b := rec.RawSample
		ev.Ifindex = binary.LittleEndian.Uint32(b[0:4])
		ev.Family = bpf.AddressFamily(b[4])
		copy(ev.Mac[:], b[5:11])
		copy(ev.IP[:], b[11:27])

		skipping := false
		if _, ok := n.receiveNeighbors.Load(int(ev.Ifindex)); !ok {
			skipping = true
		}

		var ip net.IP
		var family int
		if ev.Family == bpf.AddressFamilyIPv4 {
			ip = net.IP(ev.IP[:4])
			family = unix.AF_INET
		} else {
			ip = net.IP(ev.IP[:])
			family = unix.AF_INET6
		}

		log.Printf("BPF neighbor event: ifindex=%d family=%d ip=%s mac=%s skipped=%t",
			ev.Ifindex, family, ip.String(), net.HardwareAddr(ev.Mac[:]).String(), skipping)

		if skipping {
			continue
		}

		if err := n.replaceNeighborReachable(int(ev.Ifindex), family, ip, ev.Mac); err != nil {
			log.Printf("neigh replace failed: %v", err)
		}
	}
}

func (n *NeighborSync) replaceNeighborReachable(ifindex, family int, ip net.IP, macbuf [6]byte) error {
	if len(ip) == 0 {
		return errors.New("empty IP from event")
	}
	hw := net.HardwareAddr(macbuf[:])
	if len(hw) != hardwareAddrLen {
		return errors.New("invalid MAC from event")
	}

	link, err := n.nlOps.LinkByIndex(ifindex)
	if err != nil {
		return fmt.Errorf("failed to get link by index: %w", err)
	}

	bridgeIdx := link.Attrs().MasterIndex
	if bridgeIdx == 0 {
		return fmt.Errorf("interface %d has no master bridge (MasterIndex=0)", ifindex)
	}

	// Check existing neighbor entry to detect MAC changes.
	// Only send gratuitous ARP/NA when the MAC actually changed to avoid
	// infinite flooding loops through VXLAN (G-NA → remote BPF → G-NA → ...).
	macChanged := true
	existingNeighs, err := n.nlOps.NeighList(bridgeIdx, family)
	if err != nil {
		log.Printf("failed to list neighbors on bridge %d: %v — assuming MAC changed", bridgeIdx, err)
	}
	for i := range existingNeighs {
		if existingNeighs[i].IP.Equal(ip) {
			if bytes.Equal(existingNeighs[i].HardwareAddr, hw) {
				macChanged = false
			}
			break
		}
	}

	neigh := &netlink.Neigh{
		LinkIndex:    bridgeIdx,
		Family:       family,
		State:        netlink.NUD_REACHABLE,
		IP:           ip,
		HardwareAddr: hw,
	}
	if err := n.nlOps.NeighSet(neigh); err != nil {
		return fmt.Errorf("failed to set neighbor: %w", err)
	}

	// Send gratuitous ARP/NA so local pods learn the new MAC immediately.
	// This is critical because the bridge REACHABLE entry is ephemeral
	// (it cycles REACHABLE→STALE→FAILED→deleted), so we cannot rely
	// on the extern_learned path in handleNeighborAdd to send the G-NA
	// in time.
	if macChanged {
		if _, ok := n.sendGratuitousNeighbor.Load(bridgeIdx); ok {
			addr, ok := netip.AddrFromSlice(ip)
			if ok {
				log.Printf("MAC changed for %s on bridge %d, sending gratuitous neighbor", ip.String(), bridgeIdx)
				n.sendGratuitousNeighborFn(bridgeIdx, addr, hw)
			}
		}
	}

	return nil
}

func NewNeighborSync() *NeighborSync {
	return &NeighborSync{
		nlOps:                    &nl.Toolkit{},
		sendNeighborRequestFn:    sendNeighborRequest,
		sendGratuitousNeighborFn: sendGratuitousNeighbor,
	}
}

func (n *NeighborSync) initDefaults() {
	n.initOnce.Do(func() {
		if n.nlOps == nil {
			n.nlOps = &nl.Toolkit{}
		}
		if n.sendNeighborRequestFn == nil {
			n.sendNeighborRequestFn = sendNeighborRequest
		}
		if n.sendGratuitousNeighborFn == nil {
			n.sendGratuitousNeighborFn = sendGratuitousNeighbor
		}
		if n.bpfAttachFn == nil {
			n.bpfAttachFn = bpf.AttachNeighborHandlerToInterface
		}
		if n.bpfDetachFn == nil {
			n.bpfDetachFn = bpf.DetachNeighborHandlerFromInterface
		}
		if n.neighSubscribeFn == nil {
			n.neighSubscribeFn = netlink.NeighSubscribeWithOptions
		}
		if n.newRingbufReaderFn == nil {
			n.newRingbufReaderFn = func() (*ringbuf.Reader, error) { return ringbuf.NewReader(bpf.EbpfNeighborRingbuf()) }
		}
	})
}

// StartNeighborSync starts the neighbor synchronization process.
// a) Each netlink (non-extern_learn, non-permanent) neighbor entries are checked regularly.
//
//	This is accomplished by periodically sending ARP requests for IPv4 and Neighbor Solicitation messages for IPv6.
//
// b) BPF is attached to the "l2v." interfaces, reading all ARP responses and Neighbor Advertisements.
//
//	When a neighbor entry is detected, it is added to the neighbor table as reachable.
//
// c) When a neighbor moves to extern_learned or permanent state, it is no longer refreshed **by this instance**.
//
//	However a gratuitous ARP request or Neighbor Solicitation is generated to notify local apps.
func (n *NeighborSync) StartNeighborSync() {
	n.initDefaults()
	go n.receiveUpdates()
	go n.runNeighborCheck()
	go n.runBpfNeighborSync()
}

// EnsureARPRefresh marks the given interface ID for ARP refresh.
func (n *NeighborSync) EnsureARPRefresh(interfaceID int) {
	n.initDefaults()
	_, existing := n.neighRefreshInterfaces.Load(interfaceID)

	n.neighRefreshInterfaces.Store(interfaceID, struct{}{})

	if !existing {
		n.syncKernelNeighbors(interfaceID)
	}
}

// EnsureNeighborSuppression marks the given interface ID for neighbor suppression.
func (n *NeighborSync) EnsureNeighborSuppression(bridgeID, vethID int) error {
	n.initDefaults()

	// Validate the link exists before mutating any state, so a bad vethID
	// does not leave the maps in an inconsistent state.
	nlLink, err := n.nlOps.LinkByIndex(vethID)
	if err != nil {
		return fmt.Errorf("failed to get link by index: %w", err)
	}

	// Attach the BPF program before updating in-memory state. If attach fails
	// the maps remain unchanged, keeping a consistent view for callers. This
	// mirrors the ordering in DisableNeighborSuppression which detaches before
	// removing map entries.
	if err := n.bpfAttachFn(nlLink); err != nil {
		return fmt.Errorf("failed to attach BPF program: %w", err)
	}

	_, existing := n.sendGratuitousNeighbor.LoadOrStore(bridgeID, struct{}{})
	n.receiveNeighbors.LoadOrStore(vethID, struct{}{})

	if !existing {
		n.syncKernelNeighbors(bridgeID)
	}

	return nil
}

// DisableARPRefresh unmarks the given interface ID for ARP refresh.
func (n *NeighborSync) DisableARPRefresh(interfaceID int) {
	n.neighRefreshInterfaces.Delete(interfaceID)
}

// DisableNeighborSuppression unmarks the given interface ID for neighbor suppression.
func (n *NeighborSync) DisableNeighborSuppression(bridgeID, vethID int) error {
	n.initDefaults()

	// Detach the BPF program before removing in-memory state. If detach fails
	// the kernel-side suppression is still active and the maps should continue
	// to reflect that, so callers can observe a consistent error.
	nlLink, err := n.nlOps.LinkByIndex(vethID)
	if err != nil {
		return fmt.Errorf("failed to get link by index: %w", err)
	}
	if err := n.bpfDetachFn(nlLink); err != nil {
		var notFoundErr netlink.LinkNotFoundError
		if !errors.As(err, &notFoundErr) {
			return fmt.Errorf("failed to detach BPF program: %w", err)
		}
	}

	n.sendGratuitousNeighbor.Delete(bridgeID)
	n.receiveNeighbors.Delete(vethID)
	return nil
}
