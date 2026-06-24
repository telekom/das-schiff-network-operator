package nl

import (
	"net"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	mock_nl "github.com/telekom/das-schiff-network-operator/pkg/nl/mock"
	"github.com/vishvananda/netlink"
	vnl "github.com/vishvananda/netlink/nl"
	"go.uber.org/mock/gomock"
	"golang.org/x/sys/unix"
)

func mirrorManager(toolkit ToolkitInterface) *Manager {
	return NewManager(toolkit, &config.BaseConfig{})
}

func dummyLink(name string, index int) netlink.Link {
	return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name, Index: index}}
}

func notFound() error { return errMirrorTest }

var errMirrorTest = errorString("link not found")

type errorString string

func (e errorString) Error() string { return string(e) }

var _ = Describe("mirror helpers", func() {
	It("maps protocol names to IP protocol numbers", func() {
		Expect(*ipProtoNumber("tcp")).To(Equal(vnl.IPProto(protoTCP)))
		Expect(*ipProtoNumber("UDP")).To(Equal(vnl.IPProto(protoUDP)))
		Expect(ipProtoNumber("frobnicate")).To(BeNil())
	})

	It("detects the IP family of a prefix", func() {
		Expect(prefixFamily("10.0.0.0/8")).To(Equal(ethPIP))
		Expect(prefixFamily("fd00::/64")).To(Equal(ethPIPv6))
		Expect(prefixFamily("")).To(Equal(uint16(0)))
	})

	It("builds source interface names", func() {
		Expect(MirrorSourceL2(501)).To(Equal("vlan.501"))
		Expect(MirrorSourceVRF("external")).To(Equal("vx.external"))
	})

	It("builds a layer3 GRE link with key flags", func() {
		key := uint32(1001)
		link := greLink(&GRETunnel{Name: "gre-abc", Key: &key}, 10, 0, nil, nil)
		gre, ok := link.(*netlink.Gretun)
		Expect(ok).To(BeTrue())
		Expect(gre.IKey).To(Equal(uint32(1001)))
		Expect(gre.OKey).To(Equal(uint32(1001)))
		Expect(gre.IFlags).To(Equal(greKeyFlag))
		Expect(gre.Attrs().MasterIndex).To(Equal(10))
	})

	It("binds the tunnel to the source interface (link) when provided", func() {
		gre, ok := greLink(&GRETunnel{Name: "gre-abc"}, 10, 7, nil, nil).(*netlink.Gretun)
		Expect(ok).To(BeTrue())
		Expect(gre.Link).To(Equal(uint32(7)))

		gtap, ok := greLink(&GRETunnel{Name: "gtap-abc", Layer2: true}, 10, 7, nil, nil).(*netlink.Gretap)
		Expect(ok).To(BeTrue())
		Expect(gtap.Link).To(Equal(uint32(7)))
	})

	It("selects the GRE kind from the endpoint address family", func() {
		v4 := greLink(&GRETunnel{Name: "gre4"}, 10, 0, net.ParseIP("1.1.1.1"), net.ParseIP("2.2.2.2"))
		Expect(v4.Type()).To(Equal("gre"))

		v6 := greLink(&GRETunnel{Name: "gre6"}, 10, 0, net.ParseIP("fd00::1"), net.ParseIP("fd00::2"))
		Expect(v6.Type()).To(Equal("ip6gre"))

		v6tap := greLink(&GRETunnel{Name: "gtap6", Layer2: true}, 10, 0, net.ParseIP("fd00::1"), net.ParseIP("fd00::2"))
		Expect(v6tap.Type()).To(Equal("ip6gretap"))
	})

	It("rejects mixed-family GRE endpoints", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		// The family mismatch is caught while parsing the endpoints, before any
		// netlink lookups, so no toolkit calls are expected.
		_, err := nm.ensureGRETunnel(&GRETunnel{Name: "gre-mixed", VRF: "mirror", Local: "fd00::1", Remote: "2.2.2.2"})
		Expect(err).To(MatchError(ContainSubstring("same address family")))
	})

	It("builds a layer2 GRETAP link when requested", func() {
		link := greLink(&GRETunnel{Name: "gtap-abc", Layer2: true}, 11, 0, nil, nil)
		_, ok := link.(*netlink.Gretap)
		Expect(ok).To(BeTrue())
	})
})

var _ = Describe("ReconcileLoopbacks", func() {
	It("creates a dummy in the VRF and assigns addresses", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		vrf := dummyLink("mirror", 10)
		lo := dummyLink("lo.mir", 20)
		addr, _ := netlink.ParseAddr("10.99.0.1/32")

		gomock.InOrder(
			tk.EXPECT().LinkByName("mirror").Return(vrf, nil),
			tk.EXPECT().LinkByName("lo.mir").Return(nil, notFound()),
			tk.EXPECT().LinkAdd(gomock.Any()).Return(nil),
			tk.EXPECT().LinkByName("lo.mir").Return(lo, nil),
			tk.EXPECT().LinkSetUp(lo).Return(nil),
			tk.EXPECT().ParseAddr("10.99.0.1/32").Return(addr, nil),
			tk.EXPECT().AddrList(lo, unix.AF_UNSPEC).Return(nil, nil),
			tk.EXPECT().AddrAdd(lo, addr).Return(nil),
		)

		Expect(nm.ReconcileLoopbacks([]LoopbackConfig{
			{Name: "lo.mir", VRF: "mirror", Addresses: []string{"10.99.0.1/32"}},
		})).To(Succeed())
	})

	It("removes stale addresses left over from a previous subnet", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		vrf := dummyLink("mirror", 10)
		// Existing loopback is a dummy already enslaved to the mirror VRF (index 10).
		lo := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "lo.mir", Index: 20, MasterIndex: 10}}
		desiredAddr, _ := netlink.ParseAddr("10.99.0.1/32")
		staleAddr, _ := netlink.ParseAddr("10.50.0.1/32")
		linkLocal, _ := netlink.ParseAddr("fe80::1/64")

		gomock.InOrder(
			tk.EXPECT().LinkByName("mirror").Return(vrf, nil),
			tk.EXPECT().LinkByName("lo.mir").Return(lo, nil),
			tk.EXPECT().LinkSetUp(lo).Return(nil),
			tk.EXPECT().ParseAddr("10.99.0.1/32").Return(desiredAddr, nil),
			tk.EXPECT().AddrList(lo, unix.AF_UNSPEC).Return([]netlink.Addr{*desiredAddr, *staleAddr, *linkLocal}, nil),
			// desired already present -> no AddrAdd; stale removed; link-local kept.
			tk.EXPECT().AddrDel(lo, staleAddr).Return(nil),
		)

		Expect(nm.ReconcileLoopbacks([]LoopbackConfig{
			{Name: "lo.mir", VRF: "mirror", Addresses: []string{"10.99.0.1/32"}},
		})).To(Succeed())
	})

	It("recreates a loopback that exists in the wrong VRF", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		vrf := dummyLink("mirror", 10)
		// Existing dummy is enslaved to a different VRF (index 99) -> must be replaced.
		stale := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "lo.mir", Index: 20, MasterIndex: 99}}
		fresh := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "lo.mir", Index: 21, MasterIndex: 10}}
		addr, _ := netlink.ParseAddr("10.99.0.1/32")

		gomock.InOrder(
			tk.EXPECT().LinkByName("mirror").Return(vrf, nil),
			tk.EXPECT().LinkByName("lo.mir").Return(stale, nil),
			tk.EXPECT().LinkDel(stale).Return(nil),
			tk.EXPECT().LinkAdd(gomock.Any()).Return(nil),
			tk.EXPECT().LinkByName("lo.mir").Return(fresh, nil),
			tk.EXPECT().LinkSetUp(fresh).Return(nil),
			tk.EXPECT().ParseAddr("10.99.0.1/32").Return(addr, nil),
			tk.EXPECT().AddrList(fresh, unix.AF_UNSPEC).Return(nil, nil),
			tk.EXPECT().AddrAdd(fresh, addr).Return(nil),
		)

		Expect(nm.ReconcileLoopbacks([]LoopbackConfig{
			{Name: "lo.mir", VRF: "mirror", Addresses: []string{"10.99.0.1/32"}},
		})).To(Succeed())
	})
})

var _ = Describe("ReconcileGRETunnels", func() {
	It("creates a GRE tunnel in the VRF and returns its ifindex", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		vrf := dummyLink("mirror", 10)
		gre := dummyLink("gre-abc", 30)

		gomock.InOrder(
			tk.EXPECT().LinkByName("mirror").Return(vrf, nil),
			tk.EXPECT().LinkByName("gre-abc").Return(nil, notFound()),
			tk.EXPECT().LinkAdd(gomock.Any()).Return(nil),
			tk.EXPECT().LinkByName("gre-abc").Return(gre, nil),
			tk.EXPECT().LinkSetUp(gre).Return(nil),
		)

		idx, err := nm.ReconcileGRETunnels([]GRETunnel{
			{Name: "gre-abc", VRF: "mirror", Local: "10.99.0.1", Remote: "10.250.0.100"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(idx).To(HaveKeyWithValue("gre-abc", 30))
	})

	It("reuses an existing GRE tunnel that still matches the desired spec", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		vrf := dummyLink("mirror", 10)
		existing := &netlink.Gretun{
			LinkAttrs: netlink.LinkAttrs{Name: "gre-abc", Index: 31, MasterIndex: 10},
			Local:     net.ParseIP("10.99.0.1"),
			Remote:    net.ParseIP("10.250.0.100"),
		}

		gomock.InOrder(
			tk.EXPECT().LinkByName("mirror").Return(vrf, nil),
			tk.EXPECT().LinkByName("gre-abc").Return(existing, nil),
		)

		idx, err := nm.ReconcileGRETunnels([]GRETunnel{{Name: "gre-abc", VRF: "mirror", Local: "10.99.0.1", Remote: "10.250.0.100"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(idx).To(HaveKeyWithValue("gre-abc", 31))
	})

	It("recreates the tunnel when the collector IP changed", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		vrf := dummyLink("mirror", 10)
		stale := &netlink.Gretun{
			LinkAttrs: netlink.LinkAttrs{Name: "gre-abc", Index: 31, MasterIndex: 10},
			Local:     net.ParseIP("10.99.0.1"),
			Remote:    net.ParseIP("10.250.0.100"), // old collector
		}
		recreated := dummyLink("gre-abc", 32)

		gomock.InOrder(
			tk.EXPECT().LinkByName("mirror").Return(vrf, nil),
			tk.EXPECT().LinkByName("gre-abc").Return(stale, nil),
			tk.EXPECT().LinkDel(stale).Return(nil),
			tk.EXPECT().LinkAdd(gomock.Any()).Return(nil),
			tk.EXPECT().LinkByName("gre-abc").Return(recreated, nil),
			tk.EXPECT().LinkSetUp(recreated).Return(nil),
		)

		idx, err := nm.ReconcileGRETunnels([]GRETunnel{
			{Name: "gre-abc", VRF: "mirror", Local: "10.99.0.1", Remote: "10.250.0.200"}, // new collector
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(idx).To(HaveKeyWithValue("gre-abc", 32))
	})

	It("binds the tunnel to the source interface when set", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		vrf := dummyLink("mirror", 10)
		lo := dummyLink("lo.mir6", 42)
		gre := dummyLink("gtap-abc", 33)

		var added netlink.Link
		gomock.InOrder(
			tk.EXPECT().LinkByName("mirror").Return(vrf, nil),
			tk.EXPECT().LinkByName("lo.mir6").Return(lo, nil),
			tk.EXPECT().LinkByName("gtap-abc").Return(nil, notFound()),
			tk.EXPECT().LinkAdd(gomock.Any()).DoAndReturn(func(l netlink.Link) error { added = l; return nil }),
			tk.EXPECT().LinkByName("gtap-abc").Return(gre, nil),
			tk.EXPECT().LinkSetUp(gre).Return(nil),
		)

		_, err := nm.ReconcileGRETunnels([]GRETunnel{
			{Name: "gtap-abc", VRF: "mirror", Local: "fd00::1", Remote: "fd00::2", SourceInterface: "lo.mir6", Layer2: true},
		})
		Expect(err).ToNot(HaveOccurred())
		gtap, ok := added.(*netlink.Gretap)
		Expect(ok).To(BeTrue())
		Expect(gtap.Link).To(Equal(uint32(42)))
	})
})

var _ = Describe("ReconcileTcMirrors", func() {
	It("ensures clsact and adds a flower filter redirecting to the GRE tunnel", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		src := dummyLink("vlan.501", 5)
		var added []*netlink.Flower
		var addedQdisc netlink.Qdisc

		tk.EXPECT().LinkByName("vlan.501").Return(src, nil)
		tk.EXPECT().QdiscList(src).Return(nil, nil)
		tk.EXPECT().QdiscAdd(gomock.Any()).DoAndReturn(func(q netlink.Qdisc) error {
			addedQdisc = q
			return nil
		})
		tk.EXPECT().FilterList(src, gomock.Any()).Return(nil, nil).Times(2)
		// A "tcp" rule with no prefix has no derivable family, so a filter is
		// emitted per family (IPv4 + IPv6).
		tk.EXPECT().FilterAdd(gomock.Any()).DoAndReturn(func(f netlink.Filter) error {
			added = append(added, f.(*netlink.Flower))
			return nil
		}).Times(2)

		rules := []MirrorRule{{SourceInterface: "vlan.501", Direction: "ingress", GREInterface: "gre-abc", Protocol: "tcp"}}
		Expect(nm.ReconcileTcMirrors(rules, map[string]int{"gre-abc": 30})).To(Succeed())

		clsact, ok := addedQdisc.(*netlink.Clsact)
		Expect(ok).To(BeTrue())
		Expect(clsact.Attrs().Handle).To(Equal(netlink.MakeHandle(clsactHandleMajor, 0)))
		Expect(clsact.Attrs().Parent).To(Equal(uint32(netlink.HANDLE_CLSACT)))

		Expect(added).To(HaveLen(2))
		ethTypes := []uint16{added[0].EthType, added[1].EthType}
		Expect(ethTypes).To(ConsistOf(ethPIP, ethPIPv6))
		for _, f := range added {
			Expect(f.IPProto).ToNot(BeNil())
			Expect(*f.IPProto).To(Equal(vnl.IPProto(protoTCP)))
			Expect(f.Actions).To(HaveLen(1))
			Expect(f.Actions[0].(*netlink.MirredAction).Ifindex).To(Equal(30))
			// Distinct priorities within the same (ingress) hook.
			Expect(int(f.Priority)).To(BeNumerically(">=", mirrorFilterPriorityBase))
		}
		Expect(added[0].Priority).ToNot(Equal(added[1].Priority))
	})

	It("emits TCP/UDP/SCTP filters for both families when only a port is matched", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		src := dummyLink("vlan.501", 5)
		var added []*netlink.Flower

		tk.EXPECT().LinkByName("vlan.501").Return(src, nil)
		tk.EXPECT().QdiscList(src).Return([]netlink.Qdisc{&netlink.Clsact{}}, nil)
		tk.EXPECT().FilterList(src, gomock.Any()).Return(nil, nil).Times(2)
		// 2 families x 3 port-based protocols = 6 filters, each with the port set.
		tk.EXPECT().FilterAdd(gomock.Any()).DoAndReturn(func(f netlink.Filter) error {
			added = append(added, f.(*netlink.Flower))
			return nil
		}).Times(6)

		port := uint16(443)
		rules := []MirrorRule{{SourceInterface: "vlan.501", Direction: "ingress", GREInterface: "gre-abc", DstPort: port}}
		Expect(nm.ReconcileTcMirrors(rules, map[string]int{"gre-abc": 30})).To(Succeed())

		Expect(added).To(HaveLen(6))
		protos := map[vnl.IPProto]int{}
		prios := map[uint16]struct{}{}
		for _, f := range added {
			Expect(f.IPProto).ToNot(BeNil())
			protos[*f.IPProto]++
			Expect(f.DestPort).To(Equal(port))
			prios[f.Priority] = struct{}{}
		}
		Expect(protos).To(HaveKeyWithValue(vnl.IPProto(protoTCP), 2))
		Expect(protos).To(HaveKeyWithValue(vnl.IPProto(protoUDP), 2))
		Expect(protos).To(HaveKeyWithValue(vnl.IPProto(protoSCTP), 2))
		// All 6 share the ingress hook, so all priorities must be unique.
		Expect(prios).To(HaveLen(6))
	})

	It("emits a single family-specific filter when the prefix family is known", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		src := dummyLink("vlan.501", 5)
		var added []*netlink.Flower

		tk.EXPECT().LinkByName("vlan.501").Return(src, nil)
		tk.EXPECT().QdiscList(src).Return([]netlink.Qdisc{&netlink.Clsact{}}, nil)
		tk.EXPECT().FilterList(src, gomock.Any()).Return(nil, nil).Times(2)
		tk.EXPECT().FilterAdd(gomock.Any()).DoAndReturn(func(f netlink.Filter) error {
			added = append(added, f.(*netlink.Flower))
			return nil
		}).Times(1)

		rules := []MirrorRule{{SourceInterface: "vlan.501", Direction: "ingress", GREInterface: "gre-abc", DstPrefix: "fd00::/64"}}
		Expect(nm.ReconcileTcMirrors(rules, map[string]int{"gre-abc": 30})).To(Succeed())

		Expect(added).To(HaveLen(1))
		Expect(added[0].EthType).To(Equal(ethPIPv6))
		Expect(added[0].DestIP).ToNot(BeNil())
	})

	It("matches a bare host IP (no CIDR) as /32 or /128", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		src := dummyLink("vlan.501", 5)
		var added *netlink.Flower

		tk.EXPECT().LinkByName("vlan.501").Return(src, nil)
		tk.EXPECT().QdiscList(src).Return([]netlink.Qdisc{&netlink.Clsact{}}, nil)
		tk.EXPECT().FilterList(src, gomock.Any()).Return(nil, nil).Times(2)
		tk.EXPECT().FilterAdd(gomock.Any()).DoAndReturn(func(f netlink.Filter) error {
			added = f.(*netlink.Flower)
			return nil
		}).Times(1)

		// Bare host IP (no "/prefix") in SrcPrefix; family derivable -> IPv4 only.
		rules := []MirrorRule{{SourceInterface: "vlan.501", Direction: "ingress", GREInterface: "gre-abc", SrcPrefix: "76.4.0.4"}}
		Expect(nm.ReconcileTcMirrors(rules, map[string]int{"gre-abc": 30})).To(Succeed())

		Expect(added).ToNot(BeNil())
		Expect(added.EthType).To(Equal(ethPIP))
		Expect(added.SrcIP.Equal(net.ParseIP("76.4.0.4"))).To(BeTrue())
		ones, bits := added.SrcIPMask.Size()
		Expect(ones).To(Equal(32))
		Expect(bits).To(Equal(32))
	})

	It("maps direction to tc hooks per workload orientation", func() {
		// ingress = to-workload, egress = from-workload.
		// On a workload-facing L2 vlan port the hooks are inverted; on a
		// fabric-facing VRF vxlan port they are natural.
		cases := []struct {
			name           string
			iface          string
			direction      string
			workloadFacing bool
			wantParent     uint32
		}{
			{"L2 ingress (to-workload) -> egress hook", "vlan.501", "ingress", true, handleMinEgress},
			{"L2 egress (from-workload) -> ingress hook", "vlan.501", "egress", true, handleMinIngress},
			{"VRF ingress (to-workload) -> ingress hook", "vx.m2m", "ingress", false, handleMinIngress},
			{"VRF egress (from-workload) -> egress hook", "vx.m2m", "egress", false, handleMinEgress},
		}
		for _, tc := range cases {
			func() {
				mockctrl := gomock.NewController(GinkgoT())
				defer mockctrl.Finish()
				tk := mock_nl.NewMockToolkitInterface(mockctrl)
				nm := mirrorManager(tk)

				src := dummyLink(tc.iface, 5)
				var added *netlink.Flower
				tk.EXPECT().LinkByName(tc.iface).Return(src, nil)
				tk.EXPECT().QdiscList(src).Return([]netlink.Qdisc{&netlink.Clsact{}}, nil)
				tk.EXPECT().FilterList(src, gomock.Any()).Return(nil, nil).Times(2)
				tk.EXPECT().FilterAdd(gomock.Any()).DoAndReturn(func(f netlink.Filter) error {
					added = f.(*netlink.Flower)
					return nil
				}).Times(1)

				rules := []MirrorRule{{
					SourceInterface: tc.iface, Direction: tc.direction,
					GREInterface: "gre-abc", Protocol: "icmp", WorkloadFacing: tc.workloadFacing,
				}}
				Expect(nm.ReconcileTcMirrors(rules, map[string]int{"gre-abc": 30})).To(Succeed(), tc.name)
				Expect(added).ToNot(BeNil(), tc.name)
				Expect(added.Parent).To(Equal(tc.wantParent), tc.name)
			}()
		}
	})

	It("fails when the GRE tunnel index is unknown", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		src := dummyLink("vlan.501", 5)
		tk.EXPECT().LinkByName("vlan.501").Return(src, nil)
		tk.EXPECT().QdiscList(src).Return([]netlink.Qdisc{&netlink.Clsact{}}, nil)
		tk.EXPECT().FilterList(src, gomock.Any()).Return(nil, nil).Times(2)

		rules := []MirrorRule{{SourceInterface: "vlan.501", Direction: "ingress", GREInterface: "missing"}}
		Expect(nm.ReconcileTcMirrors(rules, map[string]int{})).ToNot(Succeed())
	})
})

var _ = Describe("CleanupMirrors", func() {
	It("deletes stale GRE tunnels not in the desired set", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		stale := dummyLink("gre-stale", 40)
		keep := dummyLink("gre-keep", 41)
		other := dummyLink("eth0", 2)

		tk.EXPECT().LinkList().Return([]netlink.Link{stale, keep, other}, nil)
		tk.EXPECT().LinkDel(stale).Return(nil)

		Expect(nm.CleanupMirrors([]GRETunnel{{Name: "gre-keep"}}, nil, nil)).To(Succeed())
	})

	It("deletes stale VRF-enslaved loopback dummies not in the desired set", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		vrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "mirror", Index: 10}}
		keepLo := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "lo.keep", Index: 20, MasterIndex: 10}}
		staleLo := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "lo.stale", Index: 21, MasterIndex: 10}}
		rootDummy := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "lo.root", Index: 22}} // not VRF-enslaved

		tk.EXPECT().LinkList().Return([]netlink.Link{vrf, keepLo, staleLo, rootDummy}, nil)
		// Only the stale VRF-enslaved loopback is removed; the root-namespace dummy
		// and the desired loopback are left untouched.
		tk.EXPECT().LinkDel(staleLo).Return(nil)

		Expect(nm.CleanupMirrors(nil, []LoopbackConfig{{Name: "lo.keep", VRF: "mirror"}}, nil)).To(Succeed())
	})

	It("never deletes dummies in VRFs that carry no mirror config", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		mirrorVrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "mirror", Index: 10}}
		clusterVrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "cluster", Index: 11}}
		mgmtVrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "mgmt", Index: 12}}
		// Legitimate non-mirror loopbacks enslaved to other VRFs must NOT be touched.
		clusterLo := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "lo.cluster", Index: 20, MasterIndex: 11}}
		mgmtLo := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "lo.mgmt", Index: 21, MasterIndex: 12}}
		keepLo := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "lo.mir", Index: 22, MasterIndex: 10}}

		tk.EXPECT().LinkList().Return([]netlink.Link{mirrorVrf, clusterVrf, mgmtVrf, clusterLo, mgmtLo, keepLo}, nil)
		// No LinkDel expected at all: the mirror VRF's only loopback is desired, and
		// the cluster/mgmt loopbacks are out of scope.

		Expect(nm.CleanupMirrors(nil, []LoopbackConfig{{Name: "lo.mir", VRF: "mirror"}}, nil)).To(Succeed())
	})

	It("clears only mirror-priority filters from a source no longer mirrored", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		src := dummyLink("vx.m2m", 7)
		fwdFilter := &netlink.Flower{FilterAttrs: netlink.FilterAttrs{Priority: 1}}
		mirrorFilter := &netlink.Flower{FilterAttrs: netlink.FilterAttrs{Priority: mirrorFilterPriorityBase}}

		tk.EXPECT().LinkList().Return([]netlink.Link{src}, nil)
		tk.EXPECT().FilterList(src, gomock.Any()).Return([]netlink.Filter{fwdFilter, mirrorFilter}, nil).Times(2)
		// Only the mirror-priority filter is deleted (once per clsact hook); the
		// forwarding filter at priority 1 is left untouched.
		tk.EXPECT().FilterDel(mirrorFilter).Return(nil).Times(2)

		Expect(nm.CleanupMirrors(nil, nil, nil)).To(Succeed())
	})
})
