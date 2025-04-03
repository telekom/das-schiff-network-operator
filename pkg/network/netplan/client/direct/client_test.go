package direct

import (
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
)

func init() {
	logrus.SetLevel(logrus.DebugLevel)
}
func TestApplySimpleValid(t *testing.T) {
	t.Skip("skipping integration tests as they may break host netplan configuration")

	g := NewGomegaWithT(t)
	client := New(Opts{NetPlanPath: "netplan"})
	state, err := netplan.NewState(`
network:
  version: 2
  ethernets:
    enp0s1:
      dhcp4: true
`)
	g.Expect(err).NotTo(HaveOccurred())
	expectedErr := fmt.Errorf("rollback")
	g.Expect(client.Apply("hint", &state, time.Second*20, func() error { return expectedErr })).To(Equal(expectedErr))
}

func TestConfigApplyInvalidIP(t *testing.T) {
	t.Skip("skipping integration tests as they may break host netplan configuration")
	g := NewGomegaWithT(t)
	client := New(Opts{NetPlanPath: "netplan"})
	state, err := netplan.NewState(`
network:
  version: 2
  ethernets:
    enp2s0:
      addresses:
        - 192.168.0.2/24
      dhcp4: false
      gateway4: 192.168.0.1.22
      nameservers:
        addresses:
          - 192.168.0.1
          - 8.8.8.8
        search:
          - workgroup
`)
	g.Expect(err).NotTo(HaveOccurred())
	err = client.Apply("hint", &state, time.Second, func() error { return nil })
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("invalid IPv4"))
}
func TestConfigApplyComplexValid(t *testing.T) {
	t.Skip("skipping integration tests as they may break host netplan configuration")
	g := NewGomegaWithT(t)
	client := New(Opts{NetPlanPath: "netplan"})
	state, err := netplan.NewState(`

network:
  version: 2
  ethernets:
    enp0s1:
      dhcp4: true
    enp2s0:
      addresses:
        - 192.168.0.2/24
      dhcp4: false
      gateway4: 192.168.0.1
      nameservers:
        addresses:
          - 192.168.0.1
          - 8.8.8.8
        search:
          - workgroup
`)
	g.Expect(err).NotTo(HaveOccurred())
	expectedErr := fmt.Errorf("rollback")
	g.Expect(client.Apply("hint", &state, time.Second, func() error { return expectedErr })).To(Equal(expectedErr))
}
func TestConfigGet(t *testing.T) {
	t.Skip("skipping integration tests as they may break host netplan configuration")
	g := NewGomegaWithT(t)
	client := New(Opts{NetPlanPath: "netplan"})
	res, err := client.Get()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res).To(Not(BeEmpty()))
}
func TestConfigSet(t *testing.T) {
	t.Skip("skipping integration tests as they may break host netplan configuration")
	g := NewGomegaWithT(t)
	client := New(Opts{NetPlanPath: "netplan"})
	res, err := client.Get()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res).To(Not(BeEmpty()))
}

func TestConfigApply(t *testing.T) {
	t.Skip("skipping integration tests as they may break host netplan configuration")

	//nolint:gocritic
	// 	g := NewGomegaWithT(t)
	// 	client := New(Opts{NetPlanPath: "netplan"})
	// 	stateYAML, err := client.Get()
	// 	g.Expect(err).NotTo(HaveOccurred())
	// 	logrus.Debugf("current netplan state yaml: %s", stateYAML)

	//nolint:gocritic
	// 	stateJSON, err := yaml.ToJSON([]byte(stateYAML))
	// 	g.Expect(err).NotTo(HaveOccurred())
	// 	state := gjson.ParseBytes(stateJSON)

	//nolint:gocritic
	// 	ethernets := state.Get("network.ethernets")
	// 	newEthernetNamePrefix := "ethtest"
	// 	newEthernetNameIndex := 0
	// 	var newEthernetName string
	// 	for {
	// 		newEthernetName = fmt.Sprintf("%s%d", newEthernetNamePrefix, newEthernetNameIndex)
	// 		if !ethernets.Get(newEthernetName).Exists() {
	// 			break
	// 		}
	// 		newEthernetNameIndex++
	// 	}

	//nolint:gocritic
	// 	logrus.Debugf("new netplan ethernet name: %s", newEthernetName)
	// 	newEthernet := `
	// addresses:
	//   - 192.168.0.2/24
	// dhcp4: false
	// gateway4: 192.168.0.1
	// nameservers:
	//   addresses:
	//     - 192.168.0.1
	//     - 8.8.8.8
	//   search:
	//     - workgroup
	// `
	// 	newEthernetJSON, err := yaml.ToJSON([]byte(newEthernet))
	// 	g.Expect(err).NotTo(HaveOccurred())
	// 	newStateJSON, err := sjson.SetRaw(string(stateJSON), fmt.Sprintf("network.ethernets.%s", newEthernetName), string(newEthernetJSON))
	// 	g.Expect(err).NotTo(HaveOccurred())
	// 	newStateYAML, err := k8syaml.JSONToYAML([]byte(newStateJSON))
	// 	g.Expect(err).NotTo(HaveOccurred())

	//nolint:gocritic
	// 	logrus.Debugf("new netplan state: %s", newStateYAML)

	//nolint:gocritic
	// g.Expect(client.Apply("test-state", string(newStateYAML), time.Second, func() error { return nil })).ShouldNot(HaveOccurred())
	// logrus.Infof("apply succeeded. rollbacking changes")
	// g.Expect(client.Apply("test-state", stateYAML, time.Second, func() error { return nil })).ShouldNot(HaveOccurred())
}
