package neighborsync

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	mock_nl "github.com/telekom/das-schiff-network-operator/pkg/nl/mock"
	"github.com/vishvananda/netlink"
	netlinkNl "github.com/vishvananda/netlink/nl"
	"go.uber.org/mock/gomock"
	"golang.org/x/sys/unix"
)

func TestNeighborSync(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NeighborSync Suite")
}

// noopNetlinkOps is a no-op implementation of nl.ToolkitInterface for tests that do not
// exercise any netlink operations (e.g. timer/map logic tests).
type noopNetlinkOps struct{}

func (noopNetlinkOps) LinkByIndex(_ int) (netlink.Link, error)     { return &netlink.Dummy{}, nil }
func (noopNetlinkOps) NeighList(_, _ int) ([]netlink.Neigh, error) { return nil, nil }
func (noopNetlinkOps) NeighSet(_ *netlink.Neigh) error             { return nil }
func (noopNetlinkOps) LinkByName(_ string) (netlink.Link, error)   { return &netlink.Dummy{}, nil }
func (noopNetlinkOps) LinkList() ([]netlink.Link, error)           { return nil, nil }
func (noopNetlinkOps) NewIPNet(_ net.IP) *net.IPNet                { return nil }
func (noopNetlinkOps) RouteListFiltered(_ int, _ *netlink.Route, _ uint64) ([]netlink.Route, error) {
	return nil, nil
}
func (noopNetlinkOps) RouteDel(_ *netlink.Route) error                        { return nil }
func (noopNetlinkOps) RouteAdd(_ *netlink.Route) error                        { return nil }
func (noopNetlinkOps) AddrList(_ netlink.Link, _ int) ([]netlink.Addr, error) { return nil, nil }
func (noopNetlinkOps) VethPeerIndex(_ *netlink.Veth) (int, error)             { return 0, nil }
func (noopNetlinkOps) ParseAddr(_ string) (*netlink.Addr, error)              { return nil, nil }
func (noopNetlinkOps) LinkDel(_ netlink.Link) error                           { return nil }
func (noopNetlinkOps) LinkSetUp(_ netlink.Link) error                         { return nil }
func (noopNetlinkOps) LinkAdd(_ netlink.Link) error                           { return nil }
func (noopNetlinkOps) AddrAdd(_ netlink.Link, _ *netlink.Addr) error          { return nil }
func (noopNetlinkOps) AddrDel(_ netlink.Link, _ *netlink.Addr) error          { return nil }
func (noopNetlinkOps) LinkSetLearning(_ netlink.Link, _ bool) error           { return nil }
func (noopNetlinkOps) LinkSetHairpin(_ netlink.Link, _ bool) error            { return nil }
func (noopNetlinkOps) ExecuteNetlinkRequest(_ *netlinkNl.NetlinkRequest, _ int, _ uint16) ([][]byte, error) {
	return nil, nil
}
func (noopNetlinkOps) LinkSetMTU(_ netlink.Link, _ int) error                       { return nil }
func (noopNetlinkOps) LinkSetDown(_ netlink.Link) error                             { return nil }
func (noopNetlinkOps) LinkSetHardwareAddr(_ netlink.Link, _ net.HardwareAddr) error { return nil }
func (noopNetlinkOps) LinkSetMasterByIndex(_ netlink.Link, _ int) error             { return nil }
func (noopNetlinkOps) LinkSetNoMaster(_ netlink.Link) error                         { return nil }
func (noopNetlinkOps) LinkGetProtinfo(_ netlink.Link) (netlink.Protinfo, error) {
	return netlink.Protinfo{}, nil
}
func (noopNetlinkOps) LinkSetMaster(_, _ netlink.Link) error { return nil }

func newTestNeighborSync(nlOps nl.ToolkitInterface) *NeighborSync {
	return &NeighborSync{
		nlOps:                    nlOps,
		sendNeighborRequestFn:    func(_ int, _ net.HardwareAddr, _ netip.Addr) {},
		sendGratuitousNeighborFn: func(_ int, _ netip.Addr, _ net.HardwareAddr) {},
	}
}

var _ = Describe("createTimerIfNotExists()", func() {
	It("stores a new timer when none exists", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
		addr := netip.MustParseAddr("10.0.0.1")

		n.createTimerIfNotExists(1, mac, addr)

		key := timerKey{LinkIndex: 1, Address: addr}
		val, ok := n.neighbors.Load(key)
		Expect(ok).To(BeTrue())
		t, ok := val.(*timer)
		Expect(ok).To(BeTrue())
		Expect(t.Address).To(Equal(mac))
		Expect(t.NextRun.After(time.Now().Add(-100 * time.Millisecond))).To(BeTrue())
	})

	It("updates the MAC address when a timer already exists", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		addr := netip.MustParseAddr("10.0.0.1")
		mac1 := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
		mac2 := net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}

		n.createTimerIfNotExists(1, mac1, addr)
		n.createTimerIfNotExists(1, mac2, addr)

		key := timerKey{LinkIndex: 1, Address: addr}
		val, _ := n.neighbors.Load(key)
		t := val.(*timer)
		Expect(t.Address).To(Equal(mac2))
	})

	It("stores separate timers for different link indices", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
		addr := netip.MustParseAddr("10.0.0.1")

		n.createTimerIfNotExists(1, mac, addr)
		n.createTimerIfNotExists(2, mac, addr)

		_, ok1 := n.neighbors.Load(timerKey{LinkIndex: 1, Address: addr})
		_, ok2 := n.neighbors.Load(timerKey{LinkIndex: 2, Address: addr})
		Expect(ok1).To(BeTrue())
		Expect(ok2).To(BeTrue())
	})
})

var _ = Describe("deleteTimerIfExists()", func() {
	It("removes an existing timer", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
		addr := netip.MustParseAddr("10.0.0.1")
		n.createTimerIfNotExists(1, mac, addr)

		n.deleteTimerIfExists(1, addr)

		_, ok := n.neighbors.Load(timerKey{LinkIndex: 1, Address: addr})
		Expect(ok).To(BeFalse())
	})

	It("is a no-op when no timer exists", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		addr := netip.MustParseAddr("10.0.0.1")

		Expect(func() { n.deleteTimerIfExists(99, addr) }).NotTo(Panic())
	})
})

var _ = Describe("handleNeighborDelete()", func() {
	It("removes the timer for the deleted neighbor", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
		addr := netip.MustParseAddr("192.168.1.1")
		n.createTimerIfNotExists(3, mac, addr)

		neigh := &netlink.Neigh{LinkIndex: 3}
		n.handleNeighborDelete(addr, neigh)

		_, ok := n.neighbors.Load(timerKey{LinkIndex: 3, Address: addr})
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("handleNeighborAdd()", func() {
	It("creates a timer when state is NUD_REACHABLE", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		addr := netip.MustParseAddr("10.1.2.3")

		neigh := &netlink.Neigh{
			LinkIndex: 5,
			State:     netlink.NUD_REACHABLE,
		}
		n.handleNeighborAdd(addr, neigh)

		_, ok := n.neighbors.Load(timerKey{LinkIndex: 5, Address: addr})
		Expect(ok).To(BeTrue())
	})

	It("does not create a timer when state is NUD_STALE (sends request instead)", func() {
		requestSent := false
		n := &NeighborSync{
			nlOps:                    &noopNetlinkOps{},
			sendGratuitousNeighborFn: func(_ int, _ netip.Addr, _ net.HardwareAddr) {},
			sendNeighborRequestFn:    func(_ int, _ net.HardwareAddr, _ netip.Addr) { requestSent = true },
		}
		addr := netip.MustParseAddr("10.1.2.3")
		mac := net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}

		neigh := &netlink.Neigh{
			LinkIndex:    5,
			State:        netlink.NUD_STALE,
			HardwareAddr: mac,
		}
		n.handleNeighborAdd(addr, neigh)

		_, ok := n.neighbors.Load(timerKey{LinkIndex: 5, Address: addr})
		Expect(ok).To(BeFalse())
		Expect(requestSent).To(BeTrue())
	})

	It("deletes timer and sends gratuitous neighbor when NTF_EXT_LEARNED and bridge registered", func() {
		gratSent := false
		n := &NeighborSync{
			nlOps:                    &noopNetlinkOps{},
			sendNeighborRequestFn:    func(_ int, _ net.HardwareAddr, _ netip.Addr) {},
			sendGratuitousNeighborFn: func(_ int, _ netip.Addr, _ net.HardwareAddr) { gratSent = true },
		}
		addr := netip.MustParseAddr("10.0.0.5")
		mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}

		n.createTimerIfNotExists(7, mac, addr)
		n.sendGratuitousNeighbor.Store(7, struct{}{})

		neigh := &netlink.Neigh{
			LinkIndex:    7,
			Flags:        netlink.NTF_EXT_LEARNED,
			HardwareAddr: mac,
		}
		n.handleNeighborAdd(addr, neigh)

		_, ok := n.neighbors.Load(timerKey{LinkIndex: 7, Address: addr})
		Expect(ok).To(BeFalse())
		Expect(gratSent).To(BeTrue())
	})

	It("deletes timer but skips gratuitous neighbor when NTF_EXT_LEARNED and bridge not registered", func() {
		gratSent := false
		n := &NeighborSync{
			nlOps:                    &noopNetlinkOps{},
			sendNeighborRequestFn:    func(_ int, _ net.HardwareAddr, _ netip.Addr) {},
			sendGratuitousNeighborFn: func(_ int, _ netip.Addr, _ net.HardwareAddr) { gratSent = true },
		}
		addr := netip.MustParseAddr("10.0.0.5")
		mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}

		n.createTimerIfNotExists(7, mac, addr)

		neigh := &netlink.Neigh{
			LinkIndex:    7,
			Flags:        netlink.NTF_EXT_LEARNED,
			HardwareAddr: mac,
		}
		n.handleNeighborAdd(addr, neigh)

		_, ok := n.neighbors.Load(timerKey{LinkIndex: 7, Address: addr})
		Expect(ok).To(BeFalse())
		Expect(gratSent).To(BeFalse())
	})
})

var _ = Describe("processUpdate()", func() {
	It("ignores updates for interfaces not in neighRefreshInterfaces", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		ip := net.ParseIP("10.0.0.1").To4()
		update := &netlink.NeighUpdate{
			Type:  unix.RTM_NEWNEIGH,
			Neigh: netlink.Neigh{LinkIndex: 99, IP: ip, State: netlink.NUD_REACHABLE},
		}
		n.processUpdate(update)

		addr, _ := netip.AddrFromSlice(ip)
		_, ok := n.neighbors.Load(timerKey{LinkIndex: 99, Address: addr})
		Expect(ok).To(BeFalse())
	})

	It("processes RTM_NEWNEIGH for tracked interfaces", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		n.neighRefreshInterfaces.Store(10, struct{}{})

		ip := net.ParseIP("10.0.0.2").To4()
		update := &netlink.NeighUpdate{
			Type: unix.RTM_NEWNEIGH,
			Neigh: netlink.Neigh{
				LinkIndex:    10,
				IP:           ip,
				State:        netlink.NUD_REACHABLE,
				HardwareAddr: net.HardwareAddr{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x01},
			},
		}
		n.processUpdate(update)

		addr, _ := netip.AddrFromSlice(ip)
		_, ok := n.neighbors.Load(timerKey{LinkIndex: 10, Address: addr})
		Expect(ok).To(BeTrue())
	})

	It("processes RTM_DELNEIGH for tracked interfaces", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		n.neighRefreshInterfaces.Store(10, struct{}{})

		ip := net.ParseIP("10.0.0.3").To4()
		mac := net.HardwareAddr{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x02}
		addr, _ := netip.AddrFromSlice(ip)
		n.createTimerIfNotExists(10, mac, addr)

		update := &netlink.NeighUpdate{
			Type:  unix.RTM_DELNEIGH,
			Neigh: netlink.Neigh{LinkIndex: 10, IP: ip},
		}
		n.processUpdate(update)

		_, ok := n.neighbors.Load(timerKey{LinkIndex: 10, Address: addr})
		Expect(ok).To(BeFalse())
	})

	It("ignores unrecognised netlink message types", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		n.neighRefreshInterfaces.Store(10, struct{}{})

		ip := net.ParseIP("10.0.0.4").To4()
		update := &netlink.NeighUpdate{
			Type:  0xDEAD,
			Neigh: netlink.Neigh{LinkIndex: 10, IP: ip, State: netlink.NUD_REACHABLE},
		}
		Expect(func() { n.processUpdate(update) }).NotTo(Panic())
	})
})

var _ = Describe("replaceNeighborReachable()", func() {
	It("returns error when IP is empty", func() {
		n := newTestNeighborSync(&noopNetlinkOps{})
		var mac [6]byte
		err := n.replaceNeighborReachable(1, unix.AF_INET, net.IP{}, mac)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty IP"))
	})

	It("returns error when LinkByIndex fails", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()

		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		nlMock.EXPECT().LinkByIndex(5).Return(nil, errors.New("link not found"))

		var mac [6]byte
		err := n.replaceNeighborReachable(5, unix.AF_INET, net.ParseIP("10.0.0.1").To4(), mac)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to get link by index"))
	})

	It("returns error when MasterIndex is zero", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()

		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		link := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{MasterIndex: 0}}
		nlMock.EXPECT().LinkByIndex(5).Return(link, nil)

		var mac [6]byte
		err := n.replaceNeighborReachable(5, unix.AF_INET, net.ParseIP("10.0.0.1").To4(), mac)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("MasterIndex=0"))
	})

	It("returns error when NeighSet fails", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()

		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		link := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{MasterIndex: 100}}
		nlMock.EXPECT().LinkByIndex(5).Return(link, nil)
		nlMock.EXPECT().NeighList(100, unix.AF_INET).Return(nil, nil)
		nlMock.EXPECT().NeighSet(gomock.Any()).Return(errors.New("set failed"))

		var mac [6]byte
		err := n.replaceNeighborReachable(5, unix.AF_INET, net.ParseIP("10.0.0.1").To4(), mac)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to set neighbor"))
	})

	It("calls NeighSet and returns no error when MAC is new", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()

		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		link := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{MasterIndex: 200}}
		nlMock.EXPECT().LinkByIndex(6).Return(link, nil)
		nlMock.EXPECT().NeighList(200, unix.AF_INET).Return(nil, nil)
		nlMock.EXPECT().NeighSet(gomock.Any()).Return(nil)

		var mac [6]byte
		mac[0] = 0xAA
		err := n.replaceNeighborReachable(6, unix.AF_INET, net.ParseIP("192.168.0.1").To4(), mac)
		Expect(err).NotTo(HaveOccurred())
	})

	It("does not send gratuitous neighbor when MAC is unchanged", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()

		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		gratSent := false
		n := &NeighborSync{
			nlOps:                    nlMock,
			sendNeighborRequestFn:    func(_ int, _ net.HardwareAddr, _ netip.Addr) {},
			sendGratuitousNeighborFn: func(_ int, _ netip.Addr, _ net.HardwareAddr) { gratSent = true },
		}
		n.sendGratuitousNeighbor.Store(200, struct{}{})

		ip := net.ParseIP("10.10.10.1").To4()
		var mac [6]byte
		copy(mac[:], []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})

		link := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{MasterIndex: 200}}
		existing := []netlink.Neigh{{IP: ip, HardwareAddr: net.HardwareAddr(mac[:])}}
		nlMock.EXPECT().LinkByIndex(7).Return(link, nil)
		nlMock.EXPECT().NeighList(200, unix.AF_INET).Return(existing, nil)
		nlMock.EXPECT().NeighSet(gomock.Any()).Return(nil)

		err := n.replaceNeighborReachable(7, unix.AF_INET, ip, mac)
		Expect(err).NotTo(HaveOccurred())
		Expect(gratSent).To(BeFalse())
	})

	It("sends gratuitous neighbor when MAC changed and bridge is registered", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()

		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		gratSent := false
		n := &NeighborSync{
			nlOps:                    nlMock,
			sendNeighborRequestFn:    func(_ int, _ net.HardwareAddr, _ netip.Addr) {},
			sendGratuitousNeighborFn: func(_ int, _ netip.Addr, _ net.HardwareAddr) { gratSent = true },
		}
		n.sendGratuitousNeighbor.Store(300, struct{}{})

		ip := net.ParseIP("10.10.10.2").To4()
		oldMac := net.HardwareAddr{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
		var newMac [6]byte
		copy(newMac[:], []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF})

		link := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{MasterIndex: 300}}
		existing := []netlink.Neigh{{IP: ip, HardwareAddr: oldMac}}
		nlMock.EXPECT().LinkByIndex(8).Return(link, nil)
		nlMock.EXPECT().NeighList(300, unix.AF_INET).Return(existing, nil)
		nlMock.EXPECT().NeighSet(gomock.Any()).Return(nil)

		err := n.replaceNeighborReachable(8, unix.AF_INET, ip, newMac)
		Expect(err).NotTo(HaveOccurred())
		Expect(gratSent).To(BeTrue())
	})
})

var _ = Describe("EnsureARPRefresh() / DisableARPRefresh()", func() {
	It("marks interface for refresh", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		nlMock.EXPECT().NeighList(42, netlink.FAMILY_ALL).Return(nil, nil)
		n := newTestNeighborSync(nlMock)
		n.EnsureARPRefresh(42)

		_, ok := n.neighRefreshInterfaces.Load(42)
		Expect(ok).To(BeTrue())
	})

	It("unmarks interface after DisableARPRefresh", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		nlMock.EXPECT().NeighList(43, netlink.FAMILY_ALL).Return(nil, nil)
		n := newTestNeighborSync(nlMock)
		n.EnsureARPRefresh(43)
		n.DisableARPRefresh(43)

		_, ok := n.neighRefreshInterfaces.Load(43)
		Expect(ok).To(BeFalse())
	})

	It("calling EnsureARPRefresh twice is idempotent", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		nlMock.EXPECT().NeighList(44, netlink.FAMILY_ALL).Return(nil, nil).Times(1)
		n := newTestNeighborSync(nlMock)

		n.EnsureARPRefresh(44)
		n.EnsureARPRefresh(44)

		_, ok := n.neighRefreshInterfaces.Load(44)
		Expect(ok).To(BeTrue())
	})
})

var _ = Describe("NewNeighborSync()", func() {
	It("returns a non-nil NeighborSync with real implementations wired", func() {
		ns := NewNeighborSync()
		Expect(ns).NotTo(BeNil())
		Expect(ns.nlOps).NotTo(BeNil())
		Expect(ns.sendNeighborRequestFn).NotTo(BeNil())
		Expect(ns.sendGratuitousNeighborFn).NotTo(BeNil())
	})
})

var _ = Describe("EnsureNeighborSuppression()", func() {
	It("returns error when LinkByIndex fails", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		// LinkByIndex is called first (before any state mutation), so no NeighList call occurs.
		nlMock.EXPECT().LinkByIndex(10).Return(nil, errors.New("no such device"))

		err := n.EnsureNeighborSuppression(5, 10)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to get link by index"))
	})

	It("does not store bridgeID/vethID when LinkByIndex fails", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		// LinkByIndex fails before state is mutated — maps must remain empty.
		nlMock.EXPECT().LinkByIndex(10).Return(nil, errors.New("no such device"))

		_ = n.EnsureNeighborSuppression(5, 10)

		_, bridgeStored := n.sendGratuitousNeighbor.Load(5)
		_, vethStored := n.receiveNeighbors.Load(10)
		Expect(bridgeStored).To(BeFalse(), "bridgeID must not be stored when validation fails")
		Expect(vethStored).To(BeFalse(), "vethID must not be stored when validation fails")
	})

	It("returns error when BPF attach fails", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		// Inject a BPF attach function that always fails.
		n.bpfAttachFn = func(_ netlink.Link) error { return errors.New("bpf attach failed") }

		fakeLink := &netlink.Dummy{}
		nlMock.EXPECT().LinkByIndex(10).Return(fakeLink, nil)
		// syncKernelNeighbors runs before bpfAttachFn on first registration.
		nlMock.EXPECT().NeighList(5, netlink.FAMILY_ALL).Return(nil, nil)

		err := n.EnsureNeighborSuppression(5, 10)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to attach BPF program"))
	})

	It("stores bridgeID/vethID even when BPF attach fails", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		// BPF attach fails after LinkByIndex succeeds — maps are populated
		// before bpfAttachFn is called in the current code path.
		n.bpfAttachFn = func(_ netlink.Link) error { return errors.New("bpf attach failed") }

		fakeLink := &netlink.Dummy{}
		nlMock.EXPECT().LinkByIndex(10).Return(fakeLink, nil)
		nlMock.EXPECT().NeighList(5, netlink.FAMILY_ALL).Return(nil, nil)

		_ = n.EnsureNeighborSuppression(5, 10)

		_, bridgeStored := n.sendGratuitousNeighbor.Load(5)
		_, vethStored := n.receiveNeighbors.Load(10)
		Expect(bridgeStored).To(BeTrue(), "bridgeID is stored before BPF attach is attempted")
		Expect(vethStored).To(BeTrue(), "vethID is stored before BPF attach is attempted")
	})

	It("stores bridgeID/vethID and calls NeighList on first registration", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		// Inject a no-op BPF attach function so EnsureNeighborSuppression can
		// complete successfully without a real kernel BPF program.
		n.bpfAttachFn = func(_ netlink.Link) error { return nil }

		fakeLink := &netlink.Dummy{}
		// LinkByIndex validates the veth exists; called with vethID=10.
		nlMock.EXPECT().LinkByIndex(10).Return(fakeLink, nil)
		// syncKernelNeighbors is triggered on first registration of bridgeID=5;
		// it calls NeighList on the bridge to seed the neighbor table.
		nlMock.EXPECT().NeighList(5, netlink.FAMILY_ALL).Return(nil, nil)

		err := n.EnsureNeighborSuppression(5, 10)
		Expect(err).NotTo(HaveOccurred())

		_, bridgeStored := n.sendGratuitousNeighbor.Load(5)
		_, vethStored := n.receiveNeighbors.Load(10)
		Expect(bridgeStored).To(BeTrue(), "bridgeID must be stored on success")
		Expect(vethStored).To(BeTrue(), "vethID must be stored on success")
	})
})

var _ = Describe("DisableNeighborSuppression()", func() {
	It("returns error when LinkByIndex fails", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		nlMock.EXPECT().LinkByIndex(10).Return(nil, errors.New("no such device"))

		err := n.DisableNeighborSuppression(5, 10)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to get link by index"))
	})

	It("preserves bridgeID and vethID when LinkByIndex fails", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		// Pre-populate state as if EnsureNeighborSuppression had been called
		n.sendGratuitousNeighbor.Store(5, struct{}{})
		n.receiveNeighbors.Store(10, struct{}{})

		// LinkByIndex fails before BPF detach — state must remain intact to stay
		// consistent with the kernel (BPF is still attached).
		nlMock.EXPECT().LinkByIndex(10).Return(nil, errors.New("no such device"))

		_ = n.DisableNeighborSuppression(5, 10)

		_, bridgeStored := n.sendGratuitousNeighbor.Load(5)
		_, vethStored := n.receiveNeighbors.Load(10)
		Expect(bridgeStored).To(BeTrue(), "bridgeID must be preserved when detach fails")
		Expect(vethStored).To(BeTrue(), "vethID must be preserved when detach fails")
	})

	It("preserves bridgeID and vethID when BPF detach fails", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		// Inject a failing BPF detach function
		n.bpfDetachFn = func(_ netlink.Link) error {
			return errors.New("bpf detach error")
		}

		// Pre-populate state as if EnsureNeighborSuppression had been called
		n.sendGratuitousNeighbor.Store(5, struct{}{})
		n.receiveNeighbors.Store(10, struct{}{})

		// LinkByIndex succeeds — BPF detach is what fails
		fakeLink := &netlink.Dummy{}
		nlMock.EXPECT().LinkByIndex(10).Return(fakeLink, nil)

		err := n.DisableNeighborSuppression(5, 10)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to detach BPF program"))

		// State must be preserved because BPF detach failed — kernel-side suppression is still active
		_, bridgeStored := n.sendGratuitousNeighbor.Load(5)
		_, vethStored := n.receiveNeighbors.Load(10)
		Expect(bridgeStored).To(BeTrue(), "bridgeID must be preserved when BPF detach fails")
		Expect(vethStored).To(BeTrue(), "vethID must be preserved when BPF detach fails")
	})

	It("removes bridgeID and vethID entries on success", func() {
		mockCtrl := gomock.NewController(GinkgoT())
		defer mockCtrl.Finish()
		nlMock := mock_nl.NewMockToolkitInterface(mockCtrl)
		n := newTestNeighborSync(nlMock)

		// Inject a succeeding BPF detach function
		n.bpfDetachFn = func(_ netlink.Link) error { return nil }

		// Pre-populate state as if EnsureNeighborSuppression had been called
		n.sendGratuitousNeighbor.Store(5, struct{}{})
		n.receiveNeighbors.Store(10, struct{}{})

		fakeLink := &netlink.Dummy{}
		nlMock.EXPECT().LinkByIndex(10).Return(fakeLink, nil)

		err := n.DisableNeighborSuppression(5, 10)
		Expect(err).NotTo(HaveOccurred())

		_, bridgeStored := n.sendGratuitousNeighbor.Load(5)
		_, vethStored := n.receiveNeighbors.Load(10)
		Expect(bridgeStored).To(BeFalse(), "bridgeID must be removed on successful disable")
		Expect(vethStored).To(BeFalse(), "vethID must be removed on successful disable")
	})
})
