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

	It("detects IPv6 prefixes", func() {
		Expect(isIPv6("10.0.0.0/8", "")).To(BeFalse())
		Expect(isIPv6("", "fd00::/64")).To(BeTrue())
	})

	It("builds source interface names", func() {
		Expect(MirrorSourceL2(501)).To(Equal("l2.501"))
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

		tk.EXPECT().LinkByName("gre-mixed").Return(nil, notFound())
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
			tk.EXPECT().AddrAdd(lo, addr).Return(nil),
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
			tk.EXPECT().LinkByName("gre-abc").Return(nil, notFound()),
			tk.EXPECT().LinkByName("mirror").Return(vrf, nil),
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

	It("reuses an existing GRE tunnel", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		tk.EXPECT().LinkByName("gre-abc").Return(dummyLink("gre-abc", 31), nil)

		idx, err := nm.ReconcileGRETunnels([]GRETunnel{{Name: "gre-abc", VRF: "mirror", Local: "10.99.0.1", Remote: "10.250.0.100"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(idx).To(HaveKeyWithValue("gre-abc", 31))
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
			tk.EXPECT().LinkByName("gtap-abc").Return(nil, notFound()),
			tk.EXPECT().LinkByName("mirror").Return(vrf, nil),
			tk.EXPECT().LinkByName("lo.mir6").Return(lo, nil),
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

		src := dummyLink("l2.501", 5)
		var added *netlink.Flower
		var addedQdisc netlink.Qdisc

		tk.EXPECT().LinkByName("l2.501").Return(src, nil)
		tk.EXPECT().QdiscList(src).Return(nil, nil)
		tk.EXPECT().QdiscAdd(gomock.Any()).DoAndReturn(func(q netlink.Qdisc) error {
			addedQdisc = q
			return nil
		})
		tk.EXPECT().FilterList(src, gomock.Any()).Return(nil, nil).Times(2)
		tk.EXPECT().FilterAdd(gomock.Any()).DoAndReturn(func(f netlink.Filter) error {
			added = f.(*netlink.Flower)
			return nil
		})

		rules := []MirrorRule{{SourceInterface: "l2.501", Direction: "ingress", GREInterface: "gre-abc", Protocol: "tcp"}}
		Expect(nm.ReconcileTcMirrors(rules, map[string]int{"gre-abc": 30})).To(Succeed())

		clsact, ok := addedQdisc.(*netlink.Clsact)
		Expect(ok).To(BeTrue())
		Expect(clsact.Attrs().Handle).To(Equal(netlink.MakeHandle(clsactHandleMajor, 0)))
		Expect(clsact.Attrs().Parent).To(Equal(uint32(netlink.HANDLE_CLSACT)))

		Expect(added).ToNot(BeNil())
		Expect(added.Actions).To(HaveLen(1))
		mirred := added.Actions[0].(*netlink.MirredAction)
		Expect(mirred.Ifindex).To(Equal(30))
	})

	It("fails when the GRE tunnel index is unknown", func() {
		mockctrl := gomock.NewController(GinkgoT())
		defer mockctrl.Finish()
		tk := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := mirrorManager(tk)

		src := dummyLink("l2.501", 5)
		tk.EXPECT().LinkByName("l2.501").Return(src, nil)
		tk.EXPECT().QdiscList(src).Return([]netlink.Qdisc{&netlink.Clsact{}}, nil)
		tk.EXPECT().FilterList(src, gomock.Any()).Return(nil, nil).Times(2)

		rules := []MirrorRule{{SourceInterface: "l2.501", Direction: "ingress", GREInterface: "missing"}}
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

		Expect(nm.CleanupMirrors([]GRETunnel{{Name: "gre-keep"}}, nil)).To(Succeed())
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

		Expect(nm.CleanupMirrors(nil, nil)).To(Succeed())
	})
})
