package nl

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	mock_nl "github.com/telekom/das-schiff-network-operator/pkg/nltoolkit/mock"
	"github.com/vishvananda/netlink"
	"go.uber.org/mock/gomock"
	"golang.org/x/sys/unix"
)

const (
	arpAccept           = "arp_accept"
	acceptUntrackedNA   = "accept_untracked_na"
	baseReachableTimeMs = "base_reachable_time"
	addrGenMode         = "addr_gen_mode"
)

var (
	mockctrl *gomock.Controller
	tmpDir   string
)

const dummyIntf = "dummy"

var _ = BeforeSuite(func() {
	var err error
	tmpDir, err = os.MkdirTemp(".", "testdata")
	Expect(err).ToNot(HaveOccurred())
	err = os.Chmod(tmpDir, 0o777)
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	err := os.RemoveAll(tmpDir)
	Expect(err).ToNot(HaveOccurred())
})

func TestNL(t *testing.T) {
	RegisterFailHandler(Fail)
	mockctrl = gomock.NewController(t)
	defer mockctrl.Finish()
	RunSpecs(t,
		"NL Suite")
}

var _ = Describe("GetUnderlayIP()", func() {
	It("returns error if cannot list addresses", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("cannot list addresses"))
		_, err := nm.GetUnderlayIP()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if number of listed addresses is not equal to 1", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{}, {}}, nil)
		_, err := nm.GetUnderlayIP()
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		_, err := nm.GetUnderlayIP()
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("ListL3()", func() {
	It("returns error if cannot list links", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error listing links"))
		_, err := nm.ListL3()
		Expect(err).To(HaveOccurred())
	})
	It("returns empty slice if there are no vrf interfaces", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Bridge{}}, nil)
		result, err := nm.ListL3()
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(BeEmpty())
	})
	It("returns no error error if cannot get bridge, vxlan and vrf links by name", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found")).Times(3)
		// netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(nil, errors.New("link not found"))
		// netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(nil, errors.New("link not found"))
		// netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(nil, errors.New("link notfound"))

		_, err := nm.ListL3()
		Expect(err).ToNot(HaveOccurred())
	})
	It("returns no error", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		_, err := nm.ListL3()
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("ListL2()", func() {
	It("returns error if cannot list links", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error listing links"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns empty slice if there are no bridge interfaces", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{}}, nil)
		result, err := nm.ListL2()
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(BeEmpty())
	})
	It("returns error if cannot get vlan ID as integer", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + dummyIntf}}}, nil)
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot get bridge link by index", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3}}}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(nil, errors.New("link not found"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot list addresses and not updating link", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3}}}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf, Index: 3}}, nil)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("failed to list addresses"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if failed to list addresses and the link is vxlan", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{
			&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3, Index: 3}},
			&netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: vxlanPrefix + "33", MasterIndex: 3, Index: 3}},
		}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(&netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: vxlanPrefix + dummyIntf, Index: 3}}, nil)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("failed to list addresses"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if failed get veth peer index during update", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{
			&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3, Index: 3}},
			&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethL2Prefix + "33", MasterIndex: 3, Index: 3}},
		}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(&netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: vxlanPrefix + dummyIntf, Index: 3}}, nil)
		netlinkMock.EXPECT().VethPeerIndex(gomock.Any()).Return(-1, errors.New("cannot get veth peer index"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if failed to get link by index of veth peer", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{
			&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3, Index: 3}},
			&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethL2Prefix + "33", MasterIndex: 3, Index: 3}},
		}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(&netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: vxlanPrefix + dummyIntf, Index: 3}}, nil)
		netlinkMock.EXPECT().VethPeerIndex(gomock.Any()).Return(0, nil)
		netlinkMock.EXPECT().LinkByIndex(0).Return(nil, errors.New("link not found"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if update succeeded but cannot list IPv4 addresses", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{
			&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3, Index: 3}},
			&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethL2Prefix + "33", MasterIndex: 3, Index: 3}},
		}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(&netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: vxlanPrefix + dummyIntf, Index: 3}}, nil)
		netlinkMock.EXPECT().VethPeerIndex(gomock.Any()).Return(0, nil)
		netlinkMock.EXPECT().LinkByIndex(0).Return(
			&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethL2Prefix + "33", MasterIndex: 3, Index: 3}}, nil,
		)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("failed to list addresses"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if update succeeded but cannot list IPv6 addresses", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{
			&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3, Index: 3}},
			&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethL2Prefix + "33", MasterIndex: 3, Index: 3}},
		}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(&netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: vxlanPrefix + dummyIntf, Index: 3}}, nil)
		netlinkMock.EXPECT().VethPeerIndex(gomock.Any()).Return(0, nil)
		netlinkMock.EXPECT().LinkByIndex(0).Return(
			&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethL2Prefix + "33", MasterIndex: 3, Index: 3}}, nil,
		)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{}, nil)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("failed to list addresses"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{
			&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3, Index: 3}},
			&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethL2Prefix + "33", MasterIndex: 3, Index: 3}},
		}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(&netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: vxlanPrefix + dummyIntf, Index: 3}}, nil)
		netlinkMock.EXPECT().VethPeerIndex(gomock.Any()).Return(0, nil)
		netlinkMock.EXPECT().LinkByIndex(0).Return(
			&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethL2Prefix + "33", MasterIndex: 3, Index: 3}}, nil,
		)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{
			{Scope: unix.RT_SCOPE_UNIVERSE},
			{Scope: unix.RT_SCOPE_HOST},
		}, nil).Times(2)
		_, err := nm.ListL2()
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("ParseIPAddresses()", func() {
	It("returns error if cannot parse address", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().ParseAddr("10.0.0.1").Return(nil, errors.New("error parsing address"))
		_, err := nm.ParseIPAddresses([]string{"10.0.0.1"})
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().ParseAddr("10.0.0.1").Return(&netlink.Addr{}, nil)
		_, err := nm.ParseIPAddresses([]string{"10.0.0.1"})
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("GetL3ByName()", func() {
	It("returns error if cannot list L3", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error listing links"))
		_, err := nm.GetL3ByName("name")
		Expect(err).To(HaveOccurred())
	})
	It("returns error if L3 was not found", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: dummyIntf}}}, nil)
		_, err := nm.GetL3ByName("name")
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		_, err := nm.GetL3ByName(dummyIntf)
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("CleanupL3()", func() {
	It("returns non empty error slice if any errors occurred", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkDel(gomock.Any()).Return(errors.New("error deleting link")).Times(4)
		err := nm.CleanupL3("name")
		Expect(err).ToNot(BeEmpty())
	})
	It("returns empty error slice if no errors occurred", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkDel(gomock.Any()).Return(nil).Times(4)
		err := nm.CleanupL3("name")
		Expect(err).To(BeEmpty())
	})
})

var _ = Describe("UpL3()", func() {
	It("returns error if cannot set link up", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(errors.New("failed to set link up"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set up bridge", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set up VRF to Default", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set up Default to VRF", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil).Times(2)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil).Times(2)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set up vxlan", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil).Times(3)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil).Times(3)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error no error", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil).Times(4)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil).Times(4)
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("findFreeTableID()", func() {
	It("returns error if cannot list L3", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error"))
		v, err := nm.findFreeTableID()
		Expect(v).To(Equal(-1))
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot find free table ID", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		links := []netlink.Link{}
		for i := vrfTableStart; i <= vrfTableEnd+1; i++ {
			links = append(links, &netlink.Vrf{Table: uint32(i), LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf + strconv.Itoa(i)}})
			netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
			netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
			netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		}
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(links, nil)

		v, err := nm.findFreeTableID()
		Expect(v).To(Equal(-1))
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		v, err := nm.findFreeTableID()
		Expect(v).To(Equal(vrfTableStart))
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("CleanupL2()", func() {
	numOfInterfaces := 3
	info := &Layer2Information{
		vxlan:         &netlink.Vxlan{},
		bridge:        &netlink.Bridge{},
		macvlanBridge: &netlink.Veth{},
	}
	It("returns slice of 3 errors", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkDel(gomock.Any()).Return(errors.New("cannot delete link")).Times(numOfInterfaces)
		errors := nm.CleanupL2(info)
		Expect(errors).To(HaveLen(numOfInterfaces))
	})
	It("returns empty slice", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkDel(gomock.Any()).Return(nil).Times(numOfInterfaces)
		errors := nm.CleanupL2(info)
		Expect(errors).To(BeEmpty())
	})
})

var _ = Describe("ReconcileL2()", func() {
	It("returns error if anycast gateway is used but anycast MAC is not set", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      nil,
		}
		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if unable to set MTU for bridge", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(errors.New("cannot set MTU"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if unable to set MTU for vxlan", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(errors.New("cannot set MTU"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if unable to set MTU for macvlanBridge", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(2)
		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(errors.New("cannot set MTU"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if unable to set MTU for macvlanHost", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(3)
		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(errors.New("cannot set MTU"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot get underlying interface and IP", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("error listing addresses"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if IPv6 was found", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.ParseIP("2001::"))}}, nil)

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if unable to set link down to change MAC address", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(errors.New("unable to set link down"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if unable to change MAC address", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(errors.New("unable to change MAC address"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if unable set link up after changing MAC address", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(errors.New("unable to set link up"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if unable set vxlan MAC address", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(errors.New("unable to change MAC address"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot get L3", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
			VRF:           "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "desired",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error listing links"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set master by index and desired VRF", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
			VRF:           "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "desired",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + desired.VRF}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkSetMasterByIndex(gomock.Any(), gomock.Any()).Return(errors.New("error setting master by index"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set no master", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
			VRF:           "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(errors.New("error setting no master"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot get bridge prot info", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
			VRF:           "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{}, errors.New("error getting prot info"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot reattach L2VNI - cannot set link down", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
			VRF:           "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{Learning: true}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(errors.New("cannot set link down"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot reattach L2VNI - cannot set link no master", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
			VRF:           "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{Learning: true}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(errors.New("cannot set link no master"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot reattach L2VNI - cannot set link master", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
			VRF:           "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{Learning: true}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetMaster(gomock.Any(), gomock.Any()).Return(errors.New("cannot set link master"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot reattach L2VNI - cannot set link master", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
			VRF:           "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{Learning: true}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetMaster(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(errors.New("cannot set link learning"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot reattach L2VNI - cannot set link up", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge:        &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:         &netlink.Vxlan{},
			macvlanBridge: &netlink.Veth{},
			macvlanHost:   &netlink.Veth{},
			VRF:           "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(4)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{Learning: true}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetMaster(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(errors.New("cannot set link up"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot setup macvlan interface - error creating macvlan interface", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		current := &Layer2Information{
			bridge: &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:  &netlink.Vxlan{},
			VRF:    "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(2)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{Learning: true}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetMaster(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(errors.New("cannot add link"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot reconcile IPs - cannot add address", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		oldProcSysNetPath := procSysNetPath

		procSysNetPath = tmpDir
		current := &Layer2Information{
			bridge: &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:  &netlink.Vxlan{},
			VRF:    "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(2)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{Learning: true}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetMaster(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(errors.New("cannot add address"))

		vlanName := fmt.Sprintf("%s%d", layer2Prefix, current.VlanID)
		vethName := fmt.Sprintf("%s%d", vethL2Prefix, current.VlanID)
		macVlanName := fmt.Sprintf("%s%d", macvlanPrefix, current.VlanID)

		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vethName)
		createInterfaceFile(addrGenModePathIPv6 + "/" + addrGenMode)

		addrGenModePathIPv6 = fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, macVlanName)
		createInterfaceFile(addrGenModePathIPv6 + "/" + addrGenMode)

		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv4 + "/" + baseReachableTimeMs)

		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv6 + "/" + baseReachableTimeMs)

		neighPathIPv4 = fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv4 + "/" + baseReachableTimeMs)

		neighPathIPv6 = fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv6 + "/" + baseReachableTimeMs)

		arpAcceptIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(arpAcceptIPv4 + "/" + arpAccept)

		acceptUntrackedNAIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(acceptUntrackedNAIPv6 + "/" + acceptUntrackedNA)

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())

		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot reconcile IPs - cannot delete address", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		oldProcSysNetPath := procSysNetPath

		procSysNetPath = tmpDir
		current := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(1, 1, 1, 1))}},
			bridge:          &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:           &netlink.Vxlan{},
			VRF:             "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(2, 2, 2, 2))}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(2)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{Learning: true}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetMaster(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrDel(gomock.Any(), gomock.Any()).Return(errors.New("cannot delete address"))

		vethName := fmt.Sprintf("%s%d", vethL2Prefix, current.VlanID)
		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vethName)
		createInterfaceFile(addrGenModePathIPv6 + "/" + addrGenMode)

		vlanName := fmt.Sprintf("%s%d", layer2Prefix, current.VlanID)
		addrGenModePathvlanIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(addrGenModePathvlanIPv6 + "/" + addrGenMode)

		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv4 + "/" + baseReachableTimeMs)

		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv6 + "/" + baseReachableTimeMs)

		neighPathIPv4 = fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv4 + "/" + baseReachableTimeMs)

		neighPathIPv6 = fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv6 + "/" + baseReachableTimeMs)

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())

		procSysNetPath = oldProcSysNetPath
	})
	It("returns no error", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		oldProcSysNetPath := procSysNetPath

		procSysNetPath = tmpDir
		current := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(1, 1, 1, 1))}},
			bridge:          &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:           &netlink.Vxlan{},
			VRF:             "current",
		}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(2, 2, 2, 2))}},
			AnycastMAC:      &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:             1399,
			VRF:             "",
		}

		netlinkMock.EXPECT().LinkSetMTU(gomock.Any(), gomock.Any()).Return(nil).Times(2)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetHardwareAddr(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkGetProtinfo(gomock.Any()).Return(netlink.Protinfo{Learning: true}, nil)
		netlinkMock.EXPECT().LinkSetDown(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetNoMaster(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetMaster(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().AddrDel(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Eq(unix.AF_INET6)).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.ParseIP("2a01::1")), Flags: unix.IFA_F_DADFAILED}}, nil)
		netlinkMock.EXPECT().AddrDel(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().AddrReplace(gomock.Any(), gomock.Any()).Return(nil)

		vethName := fmt.Sprintf("%s%d", vethL2Prefix, current.VlanID)
		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vethName)
		createInterfaceFile(addrGenModePathIPv6 + "/" + addrGenMode)

		vlanName := fmt.Sprintf("%s%d", layer2Prefix, current.VlanID)
		addrGenModePathvlanIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(addrGenModePathvlanIPv6 + "/" + addrGenMode)
		addrGenModePathvlanIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(addrGenModePathvlanIPv4 + "/" + arpAccept)

		vlanName = fmt.Sprintf("%s%d", macvlanPrefix, current.VlanID)
		addrGenModePathvlanIPv6 = fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(addrGenModePathvlanIPv6 + "/" + addrGenMode)

		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv4 + "/" + baseReachableTimeMs)

		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv6 + "/" + baseReachableTimeMs)

		neighPathIPv4 = fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv4 + "/" + baseReachableTimeMs)

		neighPathIPv6 = fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv6 + "/" + baseReachableTimeMs)

		err := nm.ReconcileL2(current, desired)
		Expect(err).ToNot(HaveOccurred())

		procSysNetPath = oldProcSysNetPath
	})
})

var _ = Describe("CreateL3()", func() {
	It("returns error if VRF name is longer than 15 characters", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		vrfInfo := VRFInformation{
			Name: "reallyLongTestNameOver15Chars",
		}
		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot find free table ID", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		vrfInfo := VRFInformation{
			Name: VrfPrefix + dummyIntf,
		}

		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error"))

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot create VRF - failed to add link", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		vrfInfo := VRFInformation{
			Name: VrfPrefix + dummyIntf,
		}

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(errors.New("failed to add link"))

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot create VRF - failed to disable EUI generation", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		vrfInfo := VRFInformation{
			Name: dummyIntf,
		}

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot create VRF - failed to set link up", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		oldProcSysNetPath := procSysNetPath
		procSysNetPath = tmpDir
		vrfInfo := VRFInformation{
			Name: dummyIntf,
		}

		vrfName := fmt.Sprintf("%s%s", VrfPrefix, dummyIntf)

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(errors.New("failed to set link up"))

		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vrfName)
		createInterfaceFile(addrGenModePathIPv6 + "/" + addrGenMode)

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create bridge - failed to add link", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		oldProcSysNetPath := procSysNetPath
		procSysNetPath = tmpDir
		vrfInfo := VRFInformation{
			Name: dummyIntf,
		}

		vrfName := fmt.Sprintf("%s%s", VrfPrefix, dummyIntf)

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		v := []netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(127, 0, 0, 1))}}
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(v, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(errors.New("failed to add link"))

		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vrfName)
		createInterfaceFile(addrGenModePathIPv6 + "/" + addrGenMode)

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create bridge - failed to disable EUI generation", func() {
		netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
		nm := NewManager(netlinkMock)
		oldProcSysNetPath := procSysNetPath
		procSysNetPath = tmpDir
		vrfInfo := VRFInformation{
			Name: dummyIntf,
		}

		vrfName := fmt.Sprintf("%s%s", VrfPrefix, dummyIntf)

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: VrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		v := []netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(127, 0, 0, 1))}}
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(v, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)

		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vrfName)
		createInterfaceFile(addrGenModePathIPv6 + "/" + addrGenMode)

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
})

func createInterfaceFile(path string) {
	err := os.MkdirAll(filepath.Dir(path), 0o777)
	Expect(err).ToNot(HaveOccurred())
	f, err := os.Create(path)
	Expect(err).ToNot(HaveOccurred())
	err = f.Close()
	Expect(err).ToNot(HaveOccurred())
	err = os.Chmod(path, 0o777)
	Expect(err).ToNot(HaveOccurred())
}
