package dbus_test

import (
	"context"
	"fmt"
	"testing"

	dbusv5 "github.com/godbus/dbus/v5"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan/client/dbus"
	mock_dbus "github.com/telekom/das-schiff-network-operator/pkg/network/netplan/client/dbus/mock"
	"go.uber.org/mock/gomock"
)

var (
	mockctrl *gomock.Controller
)

func init() {
	logrus.SetLevel(logrus.DebugLevel)
}

var _ = BeforeSuite(func() {
})

var _ = AfterSuite(func() {
})

func TestConfig(t *testing.T) {
	RegisterFailHandler(Fail)
	mockctrl = gomock.NewController(t)
	defer mockctrl.Finish()
	RunSpecs(t,
		"DBUS Client Config Suite")
}

var _ = Describe("New()", func() {
	It("returns error if cannot auth to dbus socket", func() {
		mockDbusConn := mock_dbus.NewMockIConn(mockctrl)
		mockDbusConn.EXPECT().Auth(gomock.Any()).Return(fmt.Errorf("auth error"))
		mockDbusConn.EXPECT().Close()

		_, err := dbus.New("", []string{}, dbus.Opts{SocketPath: ""}, mockDbusConn)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("auth error"))
	})

	It("returns error if cannot exchange hello message with dbus socket", func() {
		mockDbusConn := mock_dbus.NewMockIConn(mockctrl)
		mockDbusConn.EXPECT().Auth(gomock.Any()).Return(nil)
		mockDbusConn.EXPECT().Hello().Return(fmt.Errorf("hello error"))
		mockDbusConn.EXPECT().Close()

		_, err := dbus.New("", []string{}, dbus.Opts{SocketPath: ""}, mockDbusConn)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("hello error"))
	})

	It("returns no error", func() {
		mockDbusConn := mock_dbus.NewMockIConn(mockctrl)
		mockDbusConn.EXPECT().Auth(gomock.Any()).Return(nil)
		mockDbusConn.EXPECT().Hello().Return(nil)

		_, err := dbus.New("", []string{}, dbus.Opts{SocketPath: ""}, mockDbusConn)

		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("Initialize()", func() {
	It("returns no error", func() {
		mockDbusConn := mock_dbus.NewMockIConn(mockctrl)

		mockDbusConn.EXPECT().Auth(gomock.Any()).Return(nil)
		mockDbusConn.EXPECT().Hello().Return(nil)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject("{}")).Times(2)

		client, err := dbus.New("", []string{}, dbus.Opts{SocketPath: ""}, mockDbusConn)
		Expect(err).ToNot(HaveOccurred())

		_, err = client.Initialize()
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("Apply()", func() {
	It("returns error if Netplan configuration is not valid", func() {
		mockDbusConn := mock_dbus.NewMockIConn(mockctrl)

		mockDbusConn.EXPECT().Auth(gomock.Any()).Return(nil)
		mockDbusConn.EXPECT().Hello().Return(nil)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject("{}")).Times(3)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject(false))

		client, err := dbus.New("", []string{}, dbus.Opts{SocketPath: ""}, mockDbusConn)
		Expect(err).ToNot(HaveOccurred())

		config, err := client.Initialize()
		Expect(err).ToNot(HaveOccurred())

		err = config.Apply()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid netplan configuration"))
	})
	It("returns no error", func() {
		mockDbusConn := mock_dbus.NewMockIConn(mockctrl)

		mockDbusConn.EXPECT().Auth(gomock.Any()).Return(nil)
		mockDbusConn.EXPECT().Hello().Return(nil)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject("{}")).Times(3)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject(true))

		client, err := dbus.New("", []string{}, dbus.Opts{SocketPath: ""}, mockDbusConn)
		Expect(err).ToNot(HaveOccurred())

		config, err := client.Initialize()
		Expect(err).ToNot(HaveOccurred())

		err = config.Apply()
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("Set()", func() {
	It("returns error if Netplan configuration is not valid", func() {
		mockDbusConn := mock_dbus.NewMockIConn(mockctrl)

		mockDbusConn.EXPECT().Auth(gomock.Any()).Return(nil)
		mockDbusConn.EXPECT().Hello().Return(nil)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject("{}")).Times(2)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject(false))

		client, err := dbus.New("", []string{}, dbus.Opts{SocketPath: ""}, mockDbusConn)
		Expect(err).ToNot(HaveOccurred())

		config, err := client.Initialize()
		Expect(err).ToNot(HaveOccurred())

		err = config.Set(netplan.NewEmptyState())
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid netplan configuration"))
	})
	It("returns no error", func() {
		mockDbusConn := mock_dbus.NewMockIConn(mockctrl)

		mockDbusConn.EXPECT().Auth(gomock.Any()).Return(nil)
		mockDbusConn.EXPECT().Hello().Return(nil)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject("{}")).Times(2)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject(true))

		client, err := dbus.New("", []string{}, dbus.Opts{SocketPath: ""}, mockDbusConn)
		Expect(err).ToNot(HaveOccurred())

		config, err := client.Initialize()
		Expect(err).ToNot(HaveOccurred())

		err = config.Set(netplan.NewEmptyState())
		Expect(err).ToNot(HaveOccurred())
	})
})

var _ = Describe("Get()", func() {
	It("returns no error", func() {
		mockDbusConn := mock_dbus.NewMockIConn(mockctrl)

		mockDbusConn.EXPECT().Auth(gomock.Any()).Return(nil)
		mockDbusConn.EXPECT().Hello().Return(nil)
		mockDbusConn.EXPECT().Object(gomock.Any(), gomock.Any()).Return(newFakeObject("{}")).Times(3)

		client, err := dbus.New("", []string{}, dbus.Opts{SocketPath: ""}, mockDbusConn)
		Expect(err).ToNot(HaveOccurred())

		config, err := client.Initialize()
		Expect(err).ToNot(HaveOccurred())

		_, err = config.Get()
		Expect(err).ToNot(HaveOccurred())
	})
})

func newFakeObject(body interface{}) fakeObject {
	return fakeObject{
		body: body,
	}
}

type fakeObject struct {
	body interface{}
}

func (fo fakeObject) Call(string, dbusv5.Flags, ...interface{}) *dbusv5.Call {
	return &dbusv5.Call{Body: []interface{}{fo.body}}
}
func (fakeObject) CallWithContext(context.Context, string, dbusv5.Flags, ...interface{}) *dbusv5.Call {
	return nil
}
func (fakeObject) Go(string, dbusv5.Flags, chan *dbusv5.Call, ...interface{}) *dbusv5.Call {
	return nil
}
func (fakeObject) GoWithContext(context.Context, string, dbusv5.Flags, chan *dbusv5.Call, ...interface{}) *dbusv5.Call {
	return nil
}
func (fakeObject) AddMatchSignal(string, string, ...dbusv5.MatchOption) *dbusv5.Call {
	return nil
}
func (fakeObject) RemoveMatchSignal(string, string, ...dbusv5.MatchOption) *dbusv5.Call {
	return nil
}
func (fakeObject) GetProperty(string) (dbusv5.Variant, error) { return dbusv5.Variant{}, nil }
func (fakeObject) StoreProperty(string, interface{}) error    { return nil }
func (fakeObject) SetProperty(string, interface{}) error      { return nil }
func (fakeObject) Destination() string                        { return "" }
func (fakeObject) Path() dbusv5.ObjectPath                    { return "" }

//nolint:godot
// func TestConfigApplySimpleValid(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New("", []string{}, Opts{})
// 	g.Expect(err).To(BeNil())
// 	state, err := netplan.NewState(`
// network:
//   version: 2
//   ethernets:
//     enp0s1:
//       dhcp4: true
// `)
// 	g.Expect(err).NotTo(HaveOccurred())
// 	expectedErr := fmt.Errorf("rollback")
// 	err = client.Apply("hint", state, time.Second, func() error { return expectedErr })
// 	g.Expect(err).To(Equal(expectedErr))
// }

// //nolint:godot
// func TestConfigApplyInvalidIP(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
// 	g.Expect(err).To(BeNil())
// 	state, err := netplan.NewState(`
// network:
//   version: 2
//   ethernets:
//     enp2s0:
//       addresses:
//         - 192.168.0.2/24
//       dhcp4: false
//       gateway4: 192.168.0.1.22
//       nameservers:
//         addresses:
//           - 192.168.0.1
//           - 8.8.8.8
//         search:
//           - workgroup
// `)
// 	g.Expect(err).NotTo(HaveOccurred())

//nolint:godot
// 	err = client.Apply("hint", state, time.Second, func() error { return nil })
// 	g.Expect(err).To(Not(BeNil()))
// 	g.Expect(err.Error()).Should(ContainSubstring("invalid IPv4"))
// }
// func TestConfigApplyComplexValid(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
// 	g.Expect(err).To(BeNil())
// 	state, err := netplan.NewState(`
// network:
//   version: 2
//   ethernets:
//     enp0s1:
//       dhcp4: true
//     enp2s0:
//       addresses:
//         - 192.168.0.2/24
//       dhcp4: false
//       gateway4: 192.168.0.1
//       nameservers:
//         addresses:
//           - 192.168.0.1
//           - 8.8.8.8
//         search:
//           - workgroup
// `)
// 	g.Expect(err).NotTo(HaveOccurred())
// 	expectedErr := fmt.Errorf("rollback")
// 	err = client.Apply("hint", state, time.Second, func() error { return expectedErr })
// 	g.Expect(err).To(Equal(netplan.ProbeFailedError{Message: expectedErr.Error()}))
// }
// func TestConfigGet(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
// 	g.Expect(err).To(BeNil())
// 	res, err := client.Get()
// 	g.Expect(err).To(BeNil())
// 	g.Expect(res).To(Not(BeEmpty()))
// }
// func TestConfigSet(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
// 	g.Expect(err).To(BeNil())
// 	res, err := client.Get()
// 	g.Expect(err).To(BeNil())
// 	g.Expect(res).To(Not(BeEmpty()))
// }

// //nolint:godot
// func TestConfigApply(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
// 	g.Expect(err).To(BeNil())
// 	currentState, err := client.Get()
// 	g.Expect(err).To(BeNil())
// 	logrus.Debugf("current netplan state yaml: %s", currentState)
// 	newState := currentState.DeepCopy()
// 	ethtest := map[string]interface{}{
// 		"addresses": []string{
// 			"192.168.0.2/24",
// 		},
// 		"dhcp4":    false,
// 		"gateway4": "192.168.0.1",
// 		"nameservers": map[string]interface{}{
// 			"addresses": []string{
// 				"192.168.0.1",
// 				"8.8.8.8",
// 			},
// 			"search": []string{
// 				"workgroup",
// 			},
// 		},
// 	}
// 	raw, _ := k8syaml.Marshal(ethtest)
// 	newState.Network.Ethernets["ethtest"] = netplan.Device{
// 		Raw: raw,
// 	}
// 	logrus.Debugf("new netplan state: %s", newState.YAML())
// 	g.Expect(client.Apply("test-state", *newState, time.Second*15, func() error { return nil })).ShouldNot(HaveOccurred())
// 	logrus.Infof("apply succeeded. rollbacking changes")
// 	g.Expect(client.Apply("test-state", currentState, time.Second*15, func() error { return nil })).ShouldNot(HaveOccurred())
// }
// func TestConfigBridgeApply(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
// 	g.Expect(err).NotTo(HaveOccurred())
// 	currentState, err := client.Get()
// 	g.Expect(err).NotTo(HaveOccurred())
// 	state, err := netplan.NewState(`
// network:
//   version: 2
//   ethernets:
//     ens33:
//       dhcp4: true
//       mtu: 8950
//     ens38:
//       dhcp4: true
//       mtu: 8950
//   bonds:
//     bond-lan:
//       dhcp4: yes
//       interfaces: [ens33, ens38]
//       parameters:
//           mode: balance-rr
//           mii-monitor-interval: 1
//           gratuitous-arp: 5
//           fail-over-mac-policy: active
//           primary: ens33
//   bridges:
//     br2:
//       interfaces: [ bond-lan ]
//       openvswitch:
//         fail-mode: secure
//       dhcp4: false
//       mtu: 9000`)
// 	g.Expect(err).NotTo(HaveOccurred())
// 	probeFn := func() error {
// 		return nil
// 	}
// 	g.Expect(client.Apply("bridge-br2", state, time.Second*15, probeFn)).NotTo(HaveOccurred())
// 	logrus.Infof("rollbacking changes using previous state")
// 	g.Expect(client.Apply("bridge-br2", currentState, time.Second*15, probeFn)).NotTo(HaveOccurred())
// }
// func TestConfigEthernetsApply(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
// 	g.Expect(err).NotTo(HaveOccurred())
// 	g.Expect(err).NotTo(HaveOccurred())
// 	state, err := netplan.NewState(`
// network:
//   version: 2
//   ethernets:
//     ens33:
//       dhcp4: false
//       mtu: 8950
//     ens38:
//       dhcp4: false
//       mtu: 8950
//   bonds:
//     bond-lan:
//       dhcp4: yes
//       interfaces: [ens33, ens38]
//       parameters:
//           mode: balance-rr
//           mii-monitor-interval: 1
//           gratuitous-arp: 5
//           fail-over-mac-policy: active
//           primary: ens33
// `)
// 	g.Expect(err).NotTo(HaveOccurred())
// 	probeFn := func() error {
// 		return nil
// 	}
// 	g.Expect(client.Apply("eths", state, time.Second*15, probeFn)).NotTo(HaveOccurred())
// 	logrus.Infof("rollbacking changes using previous state")
// 	undostate, err := netplan.NewState(`
// network:
//   version: 2
//   ethernets:
// `)
// 	g.Expect(err).NotTo(HaveOccurred())
// 	g.Expect(client.Apply("eths", undostate, time.Second*15, probeFn)).NotTo(HaveOccurred())
// }
