package dbus

//nolint:godot
// import (
// 	"fmt"
// 	"testing"
// 	"time"

//nolint:godot
// 	. "github.com/onsi/gomega"
// 	"github.com/sirupsen/logrus"
// 	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
// 	k8syaml "sigs.k8s.io/yaml"
// )

//nolint:godot
// func init() {
// 	logrus.SetLevel(logrus.DebugLevel)
// }
// func TestConfig(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
// 	g.Expect(err).To(BeNil())
// 	res, err := client.config()
// 	g.Expect(err).To(BeNil())
// 	g.Expect(res).To(Not(BeEmpty()))
// }

//nolint:godot
// func TestConfigApplySimpleValid(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
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

//nolint:godot
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

//nolint:godot
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

//nolint:godot
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

//nolint:godot
// }
// func TestConfigEthernetsApply(t *testing.T) {
// 	g := NewGomegaWithT(t)
// 	client, err := New()
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

//nolint:godot
// 	undostate, err := netplan.NewState(`
// network:
//   version: 2
//   ethernets:
// `)
// 	g.Expect(err).NotTo(HaveOccurred())
// 	g.Expect(client.Apply("eths", undostate, time.Second*15, probeFn)).NotTo(HaveOccurred())

//nolint:godot
// }
