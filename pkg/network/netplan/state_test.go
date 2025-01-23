package netplan

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

func TestDevicesEqualBesidesArrayOrder(t *testing.T) {
	g := NewWithT(t)
	var d1, d2 Device
	d1Yaml := `
addresses:
  - 10.235.162.17/25
id: 1001
link: bond0
macaddress: 10:70:fd:cd:8a:3c
nameservers:
  addresses:
    - 10.235.119.37
    - 10.235.119.38
  search: []
routes:
  - to: 0.0.0.0/0
    via: 10.235.162.1
`
	d1Json, err := yaml.YAMLToJSON([]byte(d1Yaml))
	g.Expect(err).To(BeNil())
	d2Yaml := `
addresses:
  - 10.235.162.17/25
id: 1001
link: bond0
macaddress: 10:70:fd:cd:8a:3c
nameservers:
  addresses:
    - 10.235.119.38
    - 10.235.119.37
  search: []
routes:
  - to: 0.0.0.0/0
    via: 10.235.162.1
`
	d2Json, err := yaml.YAMLToJSON([]byte(d2Yaml))
	g.Expect(err).To(BeNil())

	g.Expect(json.Unmarshal([]byte(d1Json), &d1)).Should(Succeed())
	g.Expect(json.Unmarshal([]byte(d2Json), &d2)).Should(Succeed())

	g.Expect(d1.EqualsIgnoringSorting(d2)).To(BeTrue())
}

func TestDeviceEqualsIgnoringSorting(t *testing.T) {
	g := NewWithT(t)
	var d1, d2 Device
	d1Yaml := `
addresses:
  - 10.235.162.17/25
id: 1001
link: bond0
macaddress: 10:70:fd:cd:8a:3c
nameservers:
  addresses:
    - 10.235.119.37
    - 10.235.119.38
  search: []
routes:
  - to: 0.0.0.0/0
    via: 10.235.162.1
`
	d2Yaml := `
addresses:
  - 10.235.162.17/25
id: 1001
link: bond0
macaddress: 10:70:fd:cd:8a:3c
nameservers:
  addresses:
    - 10.235.119.38
    - 10.235.119.37
  search: []
routes:
  - to: 0.0.0.0/0
    via: 10.235.162.1
`

	g.Expect(yaml.Unmarshal([]byte(d1Yaml), &d1)).Should(Succeed())
	g.Expect(yaml.Unmarshal([]byte(d2Yaml), &d2)).Should(Succeed())

	g.Expect(d1.EqualsIgnoringSorting(d2)).To(BeTrue())
}
func TestMerge(t *testing.T) {
	g := NewWithT(t)

	s1Yaml := `
network:
  bonds:
    bond0:
      interfaces:
        - ens2f0np0
        - ens2f1np1
  vlans:
    bond0.151:
      routes:
        - to: 100.64.0.0/10
          via: 100.107.13.254
`
	s1, err := NewState(s1Yaml)
	g.Expect(err).To(BeNil())
	s2Yaml := `
network:
  bonds:
    bond0:
      interfaces:
        - newiface
  vlans:
    bond0.151:
      routes:
        - to: 0.0.0.0
          via: 128.0.0.7
`
	s2, err := NewState(s2Yaml)
	g.Expect(err).To(BeNil())
	g.Expect(s1.Merge(&s2)).Should(Succeed())

	expectedBondDeviceYaml := `
interfaces:
  - ens2f0np0
  - ens2f1np1
  - newiface
`
	var expectedBondDevice Device
	g.Expect(yaml.Unmarshal([]byte(expectedBondDeviceYaml), &expectedBondDevice)).Should(Succeed())
	g.Expect(s1.Network.Bonds["bond0"].EqualsIgnoringSorting(expectedBondDevice)).To(BeTrue())

	expectedVlanDeviceYaml := `
routes:
  - to: 100.64.0.0/10
    via: 100.107.13.254
  - to: 0.0.0.0
    via: 128.0.0.7
`
	var expectedVlanDevice Device
	g.Expect(yaml.Unmarshal([]byte(expectedVlanDeviceYaml), &expectedVlanDevice)).Should(Succeed())
	g.Expect(s1.Network.VLans["bond0.151"].EqualsIgnoringSorting(expectedVlanDevice)).To(BeTrue())

}
func TestMergeDeduplication(t *testing.T) {
	g := NewWithT(t)

	s1Yaml := `
network:
  ethernets:
    ens4:
      mtu: 5
      nameservers:
        addresses:
          - 10.235.119.37
          - 10.235.119.38
`
	s1, err := NewState(s1Yaml)
	g.Expect(err).To(BeNil())
	s2Yaml := `
network:
  ethernets:
    ens4:
      mtu: 5
      nameservers:
        addresses:
        - 10.235.119.38
        - 10.235.119.37
        - 10.235.119.39
`
	s2, err := NewState(s2Yaml)
	g.Expect(err).To(BeNil())
	g.Expect(s1.Merge(&s2)).Should(Succeed())

	expectedEthDeviceYaml := `
mtu: 5
nameservers:
  addresses:
  - 10.235.119.37
  - 10.235.119.38
  - 10.235.119.39
`
	var expectedBondDevice Device
	g.Expect(yaml.Unmarshal([]byte(expectedEthDeviceYaml), &expectedBondDevice)).Should(Succeed())
	g.Expect(s1.Network.Ethernets["ens4"].EqualsIgnoringSorting(expectedBondDevice)).To(BeTrue())
}

func TestStateEqual(t *testing.T) {
	g := NewWithT(t)
	dev1 := map[string]interface{}{
		"interfaces": []string{
			"i1",
			"i2",
		},
	}
	dev2 := map[string]interface{}{
		"interfaces": []string{
			"i2",
			"i1",
		},
	}
	dev1String, _ := yaml.Marshal(dev1)
	dev2String, _ := yaml.Marshal(dev2)

	s1 := State{
		Network: NetworkState{
			Ethernets: map[string]Device{
				"dev": {
					Raw: RawDevice(dev1String),
				},
			},
		},
	}

	s2 := State{
		Network: NetworkState{
			Ethernets: map[string]Device{
				"dev": {
					Raw: RawDevice(dev2String),
				},
			},
		},
	}
	g.Expect(s1.Equals(s2)).To(BeFalse())
}
