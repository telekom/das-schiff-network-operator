package anycast

import (
	"errors"
	"net"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	mock_nl "github.com/telekom/das-schiff-network-operator/pkg/nl/mock"
	"github.com/vishvananda/netlink"
	"go.uber.org/mock/gomock"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	mockctrl *gomock.Controller
	logger   logr.Logger
)

func TestAnycast(t *testing.T) {
	RegisterFailHandler(Fail)
	mockctrl = gomock.NewController(t)
	defer mockctrl.Finish()
	logger = ctrl.Log.WithName("anycast-test")
	RunSpecs(t,
		"Anycast Suite")
}

var _ = Describe("buildRoute()", func() {
	It("builds route", func() {
		route := buildRoute(0, &netlink.Bridge{}, &net.IPNet{}, 0)
		Expect(route).ToNot(BeNil())
	})
})

var _ = Describe("containsIPAddress()", func() {
	It("returns false if list does not contain IP address", func() {
		result := containsIPAddress([]netlink.Neigh{{IP: net.IPv4(0, 0, 0, 0)}}, &net.IPNet{IP: net.IPv4(1, 1, 1, 1)})
		Expect(result).To(BeFalse())
	})
	It("returns true if list does contain IP address", func() {
		result := containsIPAddress([]netlink.Neigh{{IP: net.IPv4(0, 0, 0, 0)}}, &net.IPNet{IP: net.IPv4(0, 0, 0, 0)})
		Expect(result).To(BeTrue())
	})
})

var _ = Describe("containsIPNetwork()", func() {
	It("returns false if list does not contain IP network", func() {
		result := containsIPNetwork([]*net.IPNet{{IP: net.IPv4(0, 0, 0, 0), Mask: net.IPv4Mask(255, 255, 255, 0)}},
			&net.IPNet{IP: net.IPv4(0, 0, 0, 0), Mask: net.IPv4Mask(255, 255, 255, 254)})
		Expect(result).To(BeFalse())
	})
	It("returns true if list does contain IP network", func() {
		result := containsIPNetwork([]*net.IPNet{{IP: net.IPv4(0, 0, 0, 0), Mask: net.IPv4Mask(255, 255, 255, 0)}},
			&net.IPNet{IP: net.IPv4(0, 0, 0, 0), Mask: net.IPv4Mask(255, 255, 255, 0)})
		Expect(result).To(BeTrue())
	})
})

var _ = Describe("filterNeighbors()", func() {
	It("returns empty list if flags not as expected", func() {
		neighIn := []netlink.Neigh{{Flags: netlink.NTF_EXT_LEARNED}}
		result := filterNeighbors(neighIn)
		Expect(result).To(BeEmpty())
	})
	It("returns empty list if state not as expected", func() {
		neighIn := []netlink.Neigh{{Flags: netlink.NTF_EXT_MANAGED, State: netlink.NUD_INCOMPLETE}}
		result := filterNeighbors(neighIn)
		Expect(result).To(BeEmpty())
	})
	It("returns non empty list", func() {
		neighIn := []netlink.Neigh{{Flags: netlink.NTF_EXT_MANAGED}}
		result := filterNeighbors(neighIn)
		Expect(result).ToNot(BeEmpty())
	})
})

var _ = Describe("syncInterfaceByFamily()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns error if cannot get neighbors", func() {
		netlinkMock.EXPECT().NeighList(0, 0).Return(nil, errors.New("fake error"))
		err := syncInterfaceByFamily(&netlink.Bridge{}, 0, 0, netlinkMock, logger)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot filter routes", func() {
		family := 0
		linkIndex := 0
		netlinkMock.EXPECT().NeighList(linkIndex, family).Return([]netlink.Neigh{{Flags: netlink.NTF_EXT_MANAGED}}, nil)
		netlinkMock.EXPECT().RouteListFiltered(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, errors.New("fake error"))
		err := syncInterfaceByFamily(&netlink.Bridge{}, family, 0, netlinkMock, logger)
		Expect(err).To(HaveOccurred())
	})
	It("returns no error if cannot delete route", func() {
		family := 0
		linkIndex := 0
		route := netlink.Route{Flags: netlink.NTF_EXT_MANAGED, Dst: netlink.NewIPNet(net.IPv4(1, 1, 1, 1))}
		netlinkMock.EXPECT().NeighList(linkIndex, family).Return([]netlink.Neigh{{Flags: netlink.NTF_EXT_MANAGED, IP: net.IPv4(0, 0, 0, 0)}}, nil)
		netlinkMock.EXPECT().RouteListFiltered(0, gomock.Any(), netlink.RT_FILTER_OIF|netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL).
			Return([]netlink.Route{route}, nil)
		netlinkMock.EXPECT().RouteDel(gomock.Any()).Return(errors.New("fake error")) // error is only logged, not returned
		netlinkMock.EXPECT().NewIPNet(gomock.Any()).Return(netlink.NewIPNet(net.IPv4(1, 1, 1, 1)))
		netlinkMock.EXPECT().RouteAdd(gomock.Any()).Return(errors.New("fake error")) // error is only logged, not returned
		err := syncInterfaceByFamily(&netlink.Bridge{}, family, 0, netlinkMock, logger)
		Expect(err).ToNot(HaveOccurred())
	})
	It("returns no error if appended route", func() {
		family := 0
		linkIndex := 0
		route := netlink.Route{Flags: netlink.NTF_EXT_MANAGED, Dst: netlink.NewIPNet(net.IPv4(1, 1, 1, 1))}
		netlinkMock.EXPECT().NeighList(linkIndex, family).Return([]netlink.Neigh{{Flags: netlink.NTF_EXT_MANAGED, IP: net.IPv4(1, 1, 1, 1)}}, nil)
		netlinkMock.EXPECT().RouteListFiltered(0, gomock.Any(), netlink.RT_FILTER_OIF|netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL).
			Return([]netlink.Route{route}, nil)
		netlinkMock.EXPECT().NewIPNet(gomock.Any()).Return(netlink.NewIPNet(net.IPv4(1, 1, 1, 1)))
		err := syncInterfaceByFamily(&netlink.Bridge{}, family, 0, netlinkMock, logger)
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("syncInterface()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns no error if interface's Master Index <= 0", func() {
		intf := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{MasterIndex: 0}}
		// returning error just to quit syncInterfaceByFamily call
		netlinkMock.EXPECT().NeighList(gomock.Any(), gomock.Any()).Return(nil, errors.New("fake error")).Times(2)
		err := syncInterface(intf, netlinkMock, logger)
		Expect(err).ToNot(HaveOccurred())
	})
	It("returns error if cannot get interface by index", func() {
		masterIndex := 1
		intf := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{MasterIndex: masterIndex}}
		netlinkMock.EXPECT().LinkByIndex(masterIndex).Return(nil, errors.New("fake error"))
		err := syncInterface(intf, netlinkMock, logger)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if could get interface but it's type is not vrf", func() {
		masterIndex := 1
		intf := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{MasterIndex: masterIndex}}
		netlinkMock.EXPECT().LinkByIndex(masterIndex).Return(&netlink.Gretun{}, nil)
		err := syncInterface(intf, netlinkMock, logger)
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		masterIndex := 1
		intf := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{MasterIndex: masterIndex}}
		netlinkMock.EXPECT().LinkByIndex(masterIndex).Return(&netlink.Vrf{}, nil)
		// returning error just to quit syncInterfaceByFamily call
		netlinkMock.EXPECT().NeighList(gomock.Any(), gomock.Any()).Return(nil, errors.New("fake error")).Times(2)
		err := syncInterface(intf, netlinkMock, logger)
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("NewTracker()", func() {
	It("returns new anycast tracker", func() {
		toolkit := &nl.Toolkit{}
		tracker := NewTracker(toolkit)
		Expect(tracker).ToNot(BeNil())
		Expect(tracker.toolkit).To(Equal(toolkit))
	})
})

var _ = Describe("checkTrackedInterfaces()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns no error if cannot get link by index", func() {
		netlinkMock.EXPECT().LinkByIndex(gomock.Any()).Return(nil, errors.New("fake error"))
		tracker := NewTracker(netlinkMock)
		Expect(tracker).ToNot(BeNil())
		Expect(tracker.toolkit).To(Equal(netlinkMock))
		tracker.TrackedBridges = []int{0}
		tracker.checkTrackedInterfaces()
	})
	It("returns no error", func() {
		netlinkMock.EXPECT().LinkByIndex(gomock.Any()).Return(&netlink.Bridge{}, nil)
		// returning error just to quit syncInterface call
		netlinkMock.EXPECT().NeighList(gomock.Any(), gomock.Any()).Return(nil, errors.New("fake error")).Times(2)
		tracker := NewTracker(netlinkMock)
		Expect(tracker).ToNot(BeNil())
		Expect(tracker.toolkit).To(Equal(netlinkMock))
		tracker.TrackedBridges = []int{0}
		tracker.checkTrackedInterfaces()
	})
})
