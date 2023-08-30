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
	mock_nl "github.com/telekom/das-schiff-network-operator/pkg/nl/mock"
	"github.com/vishvananda/netlink"
	"go.uber.org/mock/gomock"
	"golang.org/x/sys/unix"
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
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns error if cannot list addresses", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("cannot list addresses"))
		_, err := nm.GetUnderlayIP()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if number of listed addresses is not equal to 1", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{}, {}}, nil)
		_, err := nm.GetUnderlayIP()
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		_, err := nm.GetUnderlayIP()
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("ListL3()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns error if cannot list links", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error listing links"))
		_, err := nm.ListL3()
		Expect(err).To(HaveOccurred())
	})
	It("returns empty slice if there are no vrf interfaces", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Bridge{}}, nil)
		result, err := nm.ListL3()
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(BeEmpty())
	})
	It("returns no error error if cannot get bridge, vxlan and vrf links by name", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(nil, errors.New("link not found"))
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(nil, errors.New("link not found"))
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(nil, errors.New("link not found"))

		_, err := nm.ListL3()
		Expect(err).ToNot(HaveOccurred())
	})
	It("returns no error", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		_, err := nm.ListL3()
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("ListL2()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns error if cannot list links", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error listing links"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns empty slice if there are no bridge interfaces", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{}}, nil)
		result, err := nm.ListL2()
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(BeEmpty())
	})
	It("returns error if cannot get vlan ID as integer", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + dummyIntf}}}, nil)
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot get bridge link by index", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3}}}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(nil, errors.New("link not found"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot list addresses and not updating link", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: layer2Prefix + "33", MasterIndex: 3}}}, nil)
		netlinkMock.EXPECT().LinkByIndex(3).Return(&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf, Index: 3}}, nil)
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("failed to list addresses"))
		_, err := nm.ListL2()
		Expect(err).To(HaveOccurred())
	})
	It("returns error if failed to list addresses and the link is vxlan", func() {
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
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns error if cannot parse address", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().ParseAddr("10.0.0.1").Return(nil, errors.New("error parsing address"))
		_, err := nm.ParseIPAddresses([]string{"10.0.0.1"})
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().ParseAddr("10.0.0.1").Return(&netlink.Addr{}, nil)
		_, err := nm.ParseIPAddresses([]string{"10.0.0.1"})
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("GetL3ByName()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns error if cannot list L3", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error listing links"))
		_, err := nm.GetL3ByName("name")
		Expect(err).To(HaveOccurred())
	})
	It("returns error if L3 was not found", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		_, err := nm.GetL3ByName("name")
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf}}}, nil)
		_, err := nm.GetL3ByName(dummyIntf)
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("CleanupL3()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns non empty error slice if any errors occurred", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkDel(gomock.Any()).Return(errors.New("error deleting link")).Times(4)
		err := nm.CleanupL3("name")
		Expect(err).ToNot(BeEmpty())
	})
	It("returns empty error slice if no errors occurred", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkDel(gomock.Any()).Return(nil).Times(4)
		err := nm.CleanupL3("name")
		Expect(err).To(BeEmpty())
	})
})

var _ = Describe("UpL3()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns error if cannot set link up", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(errors.New("failed to set link up"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set up bridge", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set up VRF to Default", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set up Default to VRF", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil).Times(2)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil).Times(2)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set up vxlan", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil).Times(3)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil).Times(3)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).To(HaveOccurred())
	})
	It("returns error no error", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil).Times(4)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil).Times(4)
		err := nm.UpL3(VRFInformation{Name: dummyIntf})
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("findFreeTableID()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns error if cannot list L3", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error"))
		v, err := nm.findFreeTableID()
		Expect(v).To(Equal(-1))
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot find free table ID", func() {
		links := []netlink.Link{}
		for i := vrfTableStart; i <= vrfTableEnd+1; i++ {
			links = append(links, &netlink.Vrf{Table: uint32(i), LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf + strconv.Itoa(i)}})
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
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		v, err := nm.findFreeTableID()
		Expect(v).To(Equal(vrfTableStart))
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("CreateL2()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	It("returns error if cannot list L3", func() {
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error"))
		err := nm.CreateL2(&Layer2Information{
			VRF: "vrfTest",
		})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if no VRF with provided name was found", func() {
		linkName := dummyIntf
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		err := nm.CreateL2(&Layer2Information{
			VRF: "vrfTest",
		})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if anycastGateways used but anycastMAC not defined", func() {
		linkName := dummyIntf
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		err := nm.CreateL2(&Layer2Information{
			VRF:             linkName,
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}},
			AnycastMAC:      nil,
		})
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot add link for bridge", func() {
		linkName := dummyIntf
		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(errors.New("cannot add link"))
		err := nm.CreateL2(&Layer2Information{
			VRF:             linkName,
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}},
			AnycastMAC:      &net.HardwareAddr{0},
		})
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot disable EUI autogeneration for the bridge - cannot open file", func() {
		linkName := dummyIntf
		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		procSysNetPath = "invalidPath"
		err := nm.CreateL2(&Layer2Information{
			VRF:             linkName,
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}},
			AnycastMAC:      &net.HardwareAddr{0},
		})
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot disable EUI autogeneration for the bridge - cannot find link", func() {
		linkName := dummyIntf

		info := &Layer2Information{
			VRF:             linkName,
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}},
			AnycastMAC:      &net.HardwareAddr{0},
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(nil, errors.New("link not found"))

		procSysNetPath = tmpDir
		addrGenModePath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		err := os.MkdirAll(addrGenModePath, 0o777)
		Expect(err).ToNot(HaveOccurred())
		f, err := os.Create(addrGenModePath + "/addr_gen_mode")
		Expect(err).ToNot(HaveOccurred())
		err = f.Close()
		Expect(err).ToNot(HaveOccurred())
		err = os.Chmod(addrGenModePath+"/addr_gen_mode", 0o777)
		Expect(err).ToNot(HaveOccurred())

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot set ARP accept for the bridge", func() {
		linkName := dummyIntf

		info := &Layer2Information{
			VRF:             linkName,
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}},
			AnycastMAC:      &net.HardwareAddr{0},
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		addrGenModePath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		err := os.MkdirAll(addrGenModePath, 0o777)
		Expect(err).ToNot(HaveOccurred())
		f, err := os.Create(addrGenModePath + "/addr_gen_mode")
		Expect(err).ToNot(HaveOccurred())
		err = f.Close()
		Expect(err).ToNot(HaveOccurred())
		err = os.Chmod(addrGenModePath+"/addr_gen_mode", 0o777)
		Expect(err).ToNot(HaveOccurred())

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot set IPv4 base timer", func() {
		linkName := dummyIntf

		info := &Layer2Information{
			VRF:             linkName,
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}},
			AnycastMAC:      &net.HardwareAddr{0},
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		addrGenModePathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		err := os.MkdirAll(addrGenModePathIPv6, 0o777)
		Expect(err).ToNot(HaveOccurred())
		createInterfaceFile(addrGenModePathIPv6 + "/addr_gen_mode")
		createInterfaceFile(addrGenModePathIPv4 + "/arp_accept")

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot set IPv6 base timer", func() {
		linkName := dummyIntf

		info := &Layer2Information{
			VRF:             linkName,
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}},
			AnycastMAC:      &net.HardwareAddr{0},
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)

		err := os.MkdirAll(confPathIPv6, 0o777)
		Expect(err).ToNot(HaveOccurred())
		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot add address", func() {
		linkName := dummyIntf

		info := &Layer2Information{
			VRF:             linkName,
			AnycastGateways: []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}},
			AnycastMAC:      &net.HardwareAddr{0},
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(errors.New("cannot add address"))

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)

		err := os.MkdirAll(confPathIPv6, 0o777)
		Expect(err).ToNot(HaveOccurred())
		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create VXLAN - unable to list addresses", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:              linkName,
			AnycastMAC:       &net.HardwareAddr{0},
			NeighSuppression: &suppression,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)

		err := os.MkdirAll(confPathIPv6, 0o777)
		Expect(err).ToNot(HaveOccurred())
		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("error listing addresses"))

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create VXLAN - cannot generate MAC", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:              linkName,
			AnycastMAC:       &net.HardwareAddr{0},
			NeighSuppression: &suppression,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)

		err := os.MkdirAll(confPathIPv6, 0o777)
		Expect(err).ToNot(HaveOccurred())
		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv6zero)}}, nil)

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create VXLAN - cannot add link", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:              linkName,
			AnycastMAC:       &net.HardwareAddr{0},
			NeighSuppression: &suppression,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)

		err := os.MkdirAll(confPathIPv6, 0o777)
		Expect(err).ToNot(HaveOccurred())
		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(errors.New("error adding link"))

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create VXLAN - cannot set link learning", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:              linkName,
			AnycastMAC:       &net.HardwareAddr{0},
			NeighSuppression: &suppression,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)

		err := os.MkdirAll(confPathIPv6, 0o777)
		Expect(err).ToNot(HaveOccurred())
		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(errors.New("error setting link learning"))

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create VXLAN - cannot set neigh suppression", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:              linkName,
			AnycastMAC:       &net.HardwareAddr{0},
			NeighSuppression: &suppression,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)

		err := os.MkdirAll(confPathIPv6, 0o777)
		Expect(err).ToNot(HaveOccurred())
		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errors.New("error executing netlink request"))

		err = nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create VXLAN - cannot disable EUI generation for VXLAN", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:              linkName,
			AnycastMAC:       &net.HardwareAddr{0},
			NeighSuppression: &suppression,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)

		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)

		err := nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create VXLAN - cannot set VXLAN up", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:              linkName,
			AnycastMAC:       &net.HardwareAddr{0},
			NeighSuppression: &suppression,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)
		vxlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vxlanPrefix+"0")

		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")
		createInterfaceFile(vxlanConfPath + "/addr_gen_mode")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))

		err := nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create MACVLAN - cannot add link", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:                    linkName,
			AnycastMAC:             &net.HardwareAddr{0},
			NeighSuppression:       &suppression,
			CreateMACVLANInterface: true,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)
		vxlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vxlanPrefix+"0")

		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")
		createInterfaceFile(vxlanConfPath + "/addr_gen_mode")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(errors.New("error adding link"))

		err := nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create MACVLAN - cannot disable EUI generation for L2", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:                    linkName,
			AnycastMAC:             &net.HardwareAddr{0},
			NeighSuppression:       &suppression,
			CreateMACVLANInterface: true,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)
		vxlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vxlanPrefix+"0")

		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")
		createInterfaceFile(vxlanConfPath + "/addr_gen_mode")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)

		err := nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create MACVLAN - cannot disable EUI generation for vlan", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:                    linkName,
			AnycastMAC:             &net.HardwareAddr{0},
			NeighSuppression:       &suppression,
			CreateMACVLANInterface: true,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)
		vxlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vxlanPrefix+"0")
		l2ConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vethL2Prefix+"0")

		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")
		createInterfaceFile(vxlanConfPath + "/addr_gen_mode")
		createInterfaceFile(l2ConfPath + "/addr_gen_mode")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)

		err := nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create MACVLAN - cannot set up veth", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:                    linkName,
			AnycastMAC:             &net.HardwareAddr{0},
			NeighSuppression:       &suppression,
			CreateMACVLANInterface: true,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)
		vxlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vxlanPrefix+"0")
		l2ConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vethL2Prefix+"0")
		vlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, macvlanPrefix+"0")

		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")
		createInterfaceFile(vxlanConfPath + "/addr_gen_mode")
		createInterfaceFile(l2ConfPath + "/addr_gen_mode")
		createInterfaceFile(vlanConfPath + "/addr_gen_mode")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))

		err := nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create MACVLAN - cannot set up macvlan interface", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:                    linkName,
			AnycastMAC:             &net.HardwareAddr{0},
			NeighSuppression:       &suppression,
			CreateMACVLANInterface: true,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)
		vxlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vxlanPrefix+"0")
		l2ConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vethL2Prefix+"0")
		vlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, macvlanPrefix+"0")

		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")
		createInterfaceFile(vxlanConfPath + "/addr_gen_mode")
		createInterfaceFile(l2ConfPath + "/addr_gen_mode")
		createInterfaceFile(vlanConfPath + "/addr_gen_mode")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Veth{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("link not found"))

		err := nm.CreateL2(info)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create MACVLAN", func() {
		linkName := dummyIntf
		suppression := true

		info := &Layer2Information{
			VRF:                    linkName,
			AnycastMAC:             &net.HardwareAddr{0},
			NeighSuppression:       &suppression,
			CreateMACVLANInterface: true,
		}

		bridgeName := fmt.Sprintf("%s%d", layer2Prefix, info.VlanID)

		oldProcSysNetPath := procSysNetPath
		nm := NewManager(netlinkMock)
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + linkName}}}, nil)
		netlinkMock.EXPECT().LinkByName(bridgePrefix+dummyIntf).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(vxlanPrefix+dummyIntf).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(vrfToDefaultPrefix+dummyIntf).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(bridgeName).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		procSysNetPath = tmpDir
		confPathIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, bridgeName)
		confPathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, bridgeName)
		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, bridgeName)
		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, bridgeName)
		vxlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vxlanPrefix+"0")
		l2ConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vethL2Prefix+"0")
		vlanConfPath := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, macvlanPrefix+"0")

		createInterfaceFile(confPathIPv6 + "/addr_gen_mode")
		createInterfaceFile(confPathIPv4 + "/arp_accept")
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")
		createInterfaceFile(vxlanConfPath + "/addr_gen_mode")
		createInterfaceFile(l2ConfPath + "/addr_gen_mode")
		createInterfaceFile(vlanConfPath + "/addr_gen_mode")

		netlinkMock.EXPECT().AddrAdd(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))}}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetLearning(gomock.Any(), gomock.Any()).Return(nil)
		netlinkMock.EXPECT().ExecuteNetlinkRequest(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Veth{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Macvlan{}, nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)

		err := nm.CreateL2(info)
		Expect(err).ToNot(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
})

var _ = Describe("CleanupL2()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	numOfInterfaces := 3
	nm := NewManager(netlinkMock)
	info := &Layer2Information{
		vxlan:                  &netlink.Vxlan{},
		bridge:                 &netlink.Bridge{},
		CreateMACVLANInterface: true,
		macvlanBridge:          &netlink.Veth{},
	}
	It("returns slice of 3 errors", func() {
		netlinkMock.EXPECT().LinkDel(gomock.Any()).Return(errors.New("cannot delete link")).Times(numOfInterfaces)
		errors := nm.CleanupL2(info)
		Expect(errors).To(HaveLen(numOfInterfaces))
	})
	It("returns empty slice", func() {
		netlinkMock.EXPECT().LinkDel(gomock.Any()).Return(nil).Times(numOfInterfaces)
		errors := nm.CleanupL2(info)
		Expect(errors).To(BeEmpty())
	})
})

var _ = Describe("ReconcileL2()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	nm := NewManager(netlinkMock)
	It("returns error if anycast gateway is used but anycast MAC is not set", func() {
		current := &Layer2Information{}
		desired := &Layer2Information{
			AnycastGateways: []*netlink.Addr{{}},
			AnycastMAC:      nil,
		}
		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if unable to set MTU for bridge", func() {
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + desired.VRF}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkSetMasterByIndex(gomock.Any(), gomock.Any()).Return(errors.New("error setting master by index"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot set no master", func() {
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
	It("returns error if cannot setup macvlan interface - error deleting interface", func() {
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: true,
			VRF:                    "current",
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
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkDel(gomock.Any()).Return(errors.New("cannot delete interface"))

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot setup macvlan interface - error creating macvlan interface", func() {
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: false,
			VRF:                    "current",
		}
		desired := &Layer2Information{
			AnycastGateways:        []*netlink.Addr{{}},
			AnycastMAC:             &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:                    1399,
			VRF:                    "",
			CreateMACVLANInterface: true,
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
		oldProcSysNetPath := procSysNetPath

		procSysNetPath = tmpDir
		current := &Layer2Information{
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: false,
			VRF:                    "current",
		}
		desired := &Layer2Information{
			AnycastGateways:        []*netlink.Addr{{}},
			AnycastMAC:             &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:                    1399,
			VRF:                    "",
			CreateMACVLANInterface: true,
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
		createInterfaceFile(addrGenModePathIPv6 + "/addr_gen_mode")

		addrGenModePathIPv6 = fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, macVlanName)
		createInterfaceFile(addrGenModePathIPv6 + "/addr_gen_mode")

		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")

		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		neighPathIPv4 = fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")

		neighPathIPv6 = fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		arpAcceptIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(arpAcceptIPv4 + "/arp_accept")

		arpAcceptIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(arpAcceptIPv6 + "/arp_accept")

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())

		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot reconcile IPs - cannot delete address", func() {
		oldProcSysNetPath := procSysNetPath

		procSysNetPath = tmpDir
		current := &Layer2Information{
			AnycastGateways:        []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(1, 1, 1, 1))}},
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: false,
			VRF:                    "current",
		}
		desired := &Layer2Information{
			AnycastGateways:        []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(2, 2, 2, 2))}},
			AnycastMAC:             &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:                    1399,
			VRF:                    "",
			CreateMACVLANInterface: true,
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
		netlinkMock.EXPECT().AddrDel(gomock.Any(), gomock.Any()).Return(errors.New("cannot delete address"))

		vethName := fmt.Sprintf("%s%d", vethL2Prefix, current.VlanID)
		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vethName)
		createInterfaceFile(addrGenModePathIPv6 + "/addr_gen_mode")

		vlanName := fmt.Sprintf("%s%d", layer2Prefix, current.VlanID)
		addrGenModePathvlanIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(addrGenModePathvlanIPv6 + "/addr_gen_mode")

		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")

		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		neighPathIPv4 = fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")

		neighPathIPv6 = fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		err := nm.ReconcileL2(current, desired)
		Expect(err).To(HaveOccurred())

		procSysNetPath = oldProcSysNetPath
	})
	It("returns no error", func() {
		oldProcSysNetPath := procSysNetPath

		procSysNetPath = tmpDir
		current := &Layer2Information{
			AnycastGateways:        []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(1, 1, 1, 1))}},
			bridge:                 &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{HardwareAddr: net.HardwareAddr{1, 1, 1, 1, 1, 1}}},
			vxlan:                  &netlink.Vxlan{},
			macvlanBridge:          &netlink.Veth{},
			macvlanHost:            &netlink.Veth{},
			CreateMACVLANInterface: false,
			VRF:                    "current",
		}
		desired := &Layer2Information{
			AnycastGateways:        []*netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(2, 2, 2, 2))}},
			AnycastMAC:             &net.HardwareAddr{0, 0, 0, 0, 0, 0},
			MTU:                    1399,
			VRF:                    "",
			CreateMACVLANInterface: true,
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

		vethName := fmt.Sprintf("%s%d", vethL2Prefix, current.VlanID)
		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vethName)
		createInterfaceFile(addrGenModePathIPv6 + "/addr_gen_mode")

		vlanName := fmt.Sprintf("%s%d", layer2Prefix, current.VlanID)
		addrGenModePathvlanIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(addrGenModePathvlanIPv6 + "/addr_gen_mode")
		addrGenModePathvlanIPv4 := fmt.Sprintf("%s/ipv4/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(addrGenModePathvlanIPv4 + "/arp_accept")

		vlanName = fmt.Sprintf("%s%d", macvlanPrefix, current.VlanID)
		addrGenModePathvlanIPv6 = fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vlanName)
		createInterfaceFile(addrGenModePathvlanIPv6 + "/addr_gen_mode")

		neighPathIPv4 := fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")

		neighPathIPv6 := fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vethName)
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		neighPathIPv4 = fmt.Sprintf("%s/ipv4/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv4 + "/base_reachable_time_ms")

		neighPathIPv6 = fmt.Sprintf("%s/ipv6/neigh/%s", procSysNetPath, vlanName)
		createInterfaceFile(neighPathIPv6 + "/base_reachable_time_ms")

		err := nm.ReconcileL2(current, desired)
		Expect(err).ToNot(HaveOccurred())

		procSysNetPath = oldProcSysNetPath
	})
})

var _ = Describe("GetBridgeID()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	nm := NewManager(netlinkMock)
	It("returns error if cannot find link", func() {
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("error getting link by name"))
		_, err := nm.GetBridgeID(&Layer2Information{})
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		_, err := nm.GetBridgeID(&Layer2Information{})
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("GetBridgeID()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	nm := NewManager(netlinkMock)
	It("returns error if cannot find link", func() {
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(nil, errors.New("error getting link by name"))
		_, err := nm.GetBridgeID(&Layer2Information{})
		Expect(err).To(HaveOccurred())
	})
	It("returns error no error", func() {
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		_, err := nm.GetBridgeID(&Layer2Information{})
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("CreateL3()", func() {
	netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
	nm := NewManager(netlinkMock)
	It("returns error if VRF name is longer than 15 characters", func() {
		vrfInfo := VRFInformation{
			Name: "reallyLongTestNameOver15Chars",
		}
		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot find free table ID", func() {
		vrfInfo := VRFInformation{
			Name: vrfPrefix + dummyIntf,
		}

		netlinkMock.EXPECT().LinkList().Return(nil, errors.New("error"))

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot create VRF - failed to add link", func() {
		vrfInfo := VRFInformation{
			Name: vrfPrefix + dummyIntf,
		}

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(errors.New("failed to add link"))

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot create VRF - failed to disable EUI generation", func() {
		vrfInfo := VRFInformation{
			Name: dummyIntf,
		}

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
	})
	It("returns error if cannot create VRF - failed to set link up", func() {
		oldProcSysNetPath := procSysNetPath
		procSysNetPath = tmpDir
		vrfInfo := VRFInformation{
			Name: dummyIntf,
		}

		vrfName := fmt.Sprintf("%s%s", vrfPrefix, dummyIntf)

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(errors.New("failed to set link up"))

		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vrfName)
		createInterfaceFile(addrGenModePathIPv6 + "/addr_gen_mode")

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create bridge - failed to add link", func() {
		oldProcSysNetPath := procSysNetPath
		procSysNetPath = tmpDir
		vrfInfo := VRFInformation{
			Name: dummyIntf,
		}

		vrfName := fmt.Sprintf("%s%s", vrfPrefix, dummyIntf)

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		v := []netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(127, 0, 0, 1))}}
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(v, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(errors.New("failed to add link"))

		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vrfName)
		createInterfaceFile(addrGenModePathIPv6 + "/addr_gen_mode")

		err := nm.CreateL3(vrfInfo)
		Expect(err).To(HaveOccurred())
		procSysNetPath = oldProcSysNetPath
	})
	It("returns error if cannot create bridge - failed to disable EUI generation", func() {
		oldProcSysNetPath := procSysNetPath
		procSysNetPath = tmpDir
		vrfInfo := VRFInformation{
			Name: dummyIntf,
		}

		vrfName := fmt.Sprintf("%s%s", vrfPrefix, dummyIntf)

		netlinkMock.EXPECT().LinkList().Return([]netlink.Link{&netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: vrfPrefix + dummyIntf}}}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Bridge{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vxlan{}, nil)
		netlinkMock.EXPECT().LinkByName(gomock.Any()).Return(&netlink.Vrf{}, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)
		netlinkMock.EXPECT().LinkSetUp(gomock.Any()).Return(nil)
		v := []netlink.Addr{{IPNet: netlink.NewIPNet(net.IPv4(127, 0, 0, 1))}}
		netlinkMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(v, nil)
		netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(nil)

		addrGenModePathIPv6 := fmt.Sprintf("%s/ipv6/conf/%s", procSysNetPath, vrfName)
		createInterfaceFile(addrGenModePathIPv6 + "/addr_gen_mode")

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
