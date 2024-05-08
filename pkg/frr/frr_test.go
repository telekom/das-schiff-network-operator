package frr

import (
	"errors"
	"net"
	"os"
	"testing"
	"text/template"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	mock_dbus "github.com/telekom/das-schiff-network-operator/pkg/frr/dbus/mock"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	mock_nl "github.com/telekom/das-schiff-network-operator/pkg/nl/mock"
	"github.com/vishvananda/netlink"
	"go.uber.org/mock/gomock"
)

const (
	frrConf = "frr.conf"
)

var (
	mockctrl *gomock.Controller
	tmpDir   string
)

var _ = BeforeSuite(func() {
	var err error
	tmpDir, err = os.MkdirTemp(".", "testdata")
	Expect(err).ToNot(HaveOccurred())

	f, err := os.Create(tmpDir + "/" + frrConf)
	Expect(err).ToNot(HaveOccurred())
	err = f.Close()
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	err := os.RemoveAll(tmpDir)
	Expect(err).ToNot(HaveOccurred())
})

func TestFRR(t *testing.T) {
	RegisterFailHandler(Fail)
	mockctrl = gomock.NewController(t)
	defer mockctrl.Finish()
	RunSpecs(t,
		"FRR Suite")
}

var _ = Describe("frr", func() {
	Context("NewFRRManager() should", func() {
		It("create new FRR Manager", func() {
			m := NewFRRManager()
			Expect(m).ToNot(BeNil())
		})
	})
	Context("Init() should", func() {
		It("return error if cannot read template config", func() {
			m := &Manager{}
			err := m.Init()
			Expect(err).To(HaveOccurred())
		})
		It("return error if cannot write template config file", func() {
			m := &Manager{ConfigPath: "testdata/" + frrConf}
			err := m.Init()
			Expect(err).To(HaveOccurred())
		})
		It("return no error", func() {
			m := &Manager{
				ConfigPath:   tmpDir + "/" + frrConf,
				TemplatePath: tmpDir + "/frr.tpl.conf",
			}
			err := m.Init()
			Expect(err).ToNot(HaveOccurred())
		})
	})
	Context("ShouldTemplateVRF() should", func() {
		It("return false", func() {
			v := &VRFConfiguration{VNI: config.SkipVrfTemplateVni}
			result := v.ShouldTemplateVRF()
			Expect(result).To(BeFalse())
		})
		It("return true", func() {
			v := &VRFConfiguration{VNI: 0}
			result := v.ShouldTemplateVRF()
			Expect(result).To(BeTrue())
		})
	})
	Context("ShouldDefineRT() should", func() {
		It("return false", func() {
			v := &VRFConfiguration{RT: ""}
			result := v.ShouldDefineRT()
			Expect(result).To(BeFalse())
		})
		It("return true", func() {
			v := &VRFConfiguration{RT: "value"}
			result := v.ShouldDefineRT()
			Expect(result).To(BeTrue())
		})
	})
	Context("ReloadFRR() should", func() {
		dbusMock := mock_dbus.NewMockSystem(mockctrl)
		dbusConnMock := mock_dbus.NewMockConnection(mockctrl)
		m := &Manager{
			dbusToolkit: dbusMock,
		}
		It("return error if cannot create new D-Bus connection", func() {
			dbusMock.EXPECT().NewConn(gomock.Any()).Return(nil, errors.New("error creating new connection"))
			err := m.ReloadFRR()
			Expect(err).To(HaveOccurred())
		})
		It("return error if cannot reload FRR unit", func() {
			dbusMock.EXPECT().NewConn(gomock.Any()).Return(dbusConnMock, nil)
			dbusConnMock.EXPECT().ReloadUnitContext(gomock.Any(), frrUnit, "fail", gomock.Any()).Return(-1, errors.New("error reloading context"))
			dbusConnMock.EXPECT().Close()
			err := m.ReloadFRR()
			Expect(err).To(HaveOccurred())
		})
		// It("return no error", func() {
		// 	dbusMock.EXPECT().NewConn(gomock.Any()).Return(dbusConnMock, nil)
		// 	dbusConnMock.EXPECT().ReloadUnitContext(gomock.Any(), frrUnit, "fail", gomock.Any()).Return(0, nil)
		// 	dbusConnMock.EXPECT().Close()
		// 	err := m.ReloadFRR()
		// 	Expect(err).ToNot(HaveOccurred())
		// })
	})
	Context("Configure() should", func() {
		nlMock := mock_nl.NewMockToolkitInterface(mockctrl)
		It("return error if cannot get underlay IP", func() {
			m := &Manager{}
			nlMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return(nil, errors.New("error listing addresses"))
			_, err := m.Configure(Configuration{}, nl.NewManager(nlMock))
			Expect(err).To(HaveOccurred())
		})
		It("return error if cannot node's name", func() {
			oldEnv, isSet := os.LookupEnv(healthcheck.NodenameEnv)
			if isSet {
				err := os.Unsetenv(healthcheck.NodenameEnv)
				Expect(err).ToNot(HaveOccurred())
			}

			m := &Manager{}
			nlMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{
				{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))},
			}, nil)
			_, err := m.Configure(Configuration{}, nl.NewManager(nlMock))
			Expect(err).To(HaveOccurred())

			if isSet {
				err := os.Setenv(healthcheck.NodenameEnv, oldEnv)
				Expect(err).ToNot(HaveOccurred())
			}
		})
		It("return error if cannot read config file", func() {
			oldEnv, isSet := os.LookupEnv(healthcheck.NodenameEnv)
			err := os.Setenv(healthcheck.NodenameEnv, "test")
			Expect(err).ToNot(HaveOccurred())

			m := &Manager{}
			nlMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{
				{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))},
			}, nil)
			_, err = m.Configure(Configuration{}, nl.NewManager(nlMock))
			Expect(err).To(HaveOccurred())

			if isSet {
				err := os.Setenv(healthcheck.NodenameEnv, oldEnv)
				Expect(err).ToNot(HaveOccurred())
			} else {
				err := os.Unsetenv(healthcheck.NodenameEnv)
				Expect(err).ToNot(HaveOccurred())
			}
		})
		It("return error if cannot render target config", func() {
			oldEnv, isSet := os.LookupEnv(healthcheck.NodenameEnv)
			err := os.Setenv(healthcheck.NodenameEnv, "test")
			Expect(err).ToNot(HaveOccurred())

			m := &Manager{
				ConfigPath:     tmpDir + "/frr.conf",
				configTemplate: &template.Template{},
			}

			nlMock.EXPECT().AddrList(gomock.Any(), gomock.Any()).Return([]netlink.Addr{
				{IPNet: netlink.NewIPNet(net.IPv4(0, 0, 0, 0))},
			}, nil)
			_, err = m.Configure(Configuration{}, nl.NewManager(nlMock))
			Expect(err).To(HaveOccurred())

			if isSet {
				err := os.Setenv(healthcheck.NodenameEnv, oldEnv)
				Expect(err).ToNot(HaveOccurred())
			} else {
				err := os.Unsetenv(healthcheck.NodenameEnv)
				Expect(err).ToNot(HaveOccurred())
			}
		})
	})
})
