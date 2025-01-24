package frr

import (
	"errors"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	mock_dbus "github.com/telekom/das-schiff-network-operator/pkg/frr/dbus/mock"
	"go.uber.org/mock/gomock"
	"os"
	"testing"
)

const (
	frrConf = "frr.conf"
	mgmtVrf = "mgmt"
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
})
