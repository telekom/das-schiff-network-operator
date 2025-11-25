/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cra

import (
	"encoding/xml"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/types"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCraVsr(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CRA-VSR test suite")
}

const operatorConfigEnv = "OPERATOR_CONFIG"
const newConfigPath = "/tmp/config.yaml"

var oldConfigPath string
var isConfigEnvExist bool

var manager = &Manager{
	workNS: "hbn",
	running: &VRouter{
		Namespaces: []Namespace{
			{
				Name: "main",
			}, {
				Name: "hbn",
				VRFs: []VRF{
					{
						Name:    "cluster",
						TableID: 10,
						Interfaces: &Interfaces{
							Infras: []Infrastructure{
								{
									Name: "hbn",
								},
							},
						},
					}, {
						Name:    "mgmt",
						TableID: 11,
					},
				},
			},
		},
	},
	baseConfig: &config.BaseConfig{
		VTEPLoopbackIP:     "10.50.0.10",
		TrunkInterfaceName: "hbn",
		ExportCIDRs:        []string{"10.100.0.0/24", "fdcb:f93c:3a3e::/64"},
		ManagementVRF: config.BaseVRF{
			Name:            "mgmt",
			VNI:             20,
			EVPNRouteTarget: "64497:20",
		},
		ClusterVRF: config.BaseVRF{
			Name:            "cluster",
			VNI:             30,
			EVPNRouteTarget: "64497:30",
		},
		LocalASN: 64497,
		UnderlayNeighbors: []config.Neighbor{
			{
				Interface:     types.ToPtr("ens3"),
				RemoteASN:     "65500",
				LocalASN:      types.ToPtr("65501"),
				KeepaliveTime: 30,
				HoldTime:      90,
				BFDMinTimer:   types.ToPtr(333),
				IPv4:          true,
				EVPN:          false,
			}, {
				Interface:     types.ToPtr("ens4"),
				RemoteASN:     "65500",
				LocalASN:      types.ToPtr("65501"),
				KeepaliveTime: 30,
				HoldTime:      90,
				BFDMinTimer:   types.ToPtr(333),
				IPv4:          true,
				EVPN:          false,
			}, {
				IP:            types.ToPtr("192.0.2.1"),
				RemoteASN:     "64497",
				KeepaliveTime: 30,
				HoldTime:      90,
				BFDMinTimer:   types.ToPtr(333),
				IPv4:          false,
				EVPN:          true,
			}, {
				IP:            types.ToPtr("192.0.2.2"),
				RemoteASN:     "64497",
				KeepaliveTime: 30,
				HoldTime:      90,
				BFDMinTimer:   types.ToPtr(333),
				IPv4:          false,
				EVPN:          true,
			},
		},
		ClusterNeighbors: []config.Neighbor{
			{
				IP:            types.ToPtr("10.100.0.10"),
				UpdateSource:  types.ToPtr("169.254.100.100"),
				RemoteASN:     "65170",
				LocalASN:      types.ToPtr("65169"),
				KeepaliveTime: 30,
				HoldTime:      90,
				BFDMinTimer:   types.ToPtr(333),
				IPv4:          true,
			}, {
				IP:            types.ToPtr("fdcb:f93c:3a3e::10"),
				UpdateSource:  types.ToPtr("fd00:7:caa5:1::"),
				RemoteASN:     "65170",
				LocalASN:      types.ToPtr("65169"),
				KeepaliveTime: 30,
				HoldTime:      90,
				BFDMinTimer:   types.ToPtr(333),
				IPv6:          true,
			}, {
				IP:            types.ToPtr("fd00:7:caa5::1"),
				RemoteASN:     "65170",
				LocalASN:      types.ToPtr("65169"),
				KeepaliveTime: 30,
				HoldTime:      90,
				BFDMinTimer:   types.ToPtr(333),
				IPv4:          true,
				IPv6:          true,
			},
		},
	},
}

var revision = &v1alpha1.NetworkConfigRevision{
	Spec: v1alpha1.NetworkConfigRevisionSpec{
		Layer2: []v1alpha1.Layer2Revision{
			{
				Name: "vlan1",
				Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
					ID:              501,
					MTU:             1500,
					VNI:             4000002,
					VRF:             "m2m",
					AnycastMac:      "1a:ee:cf:2f:a7:a8",
					AnycastGateways: []string{"10.250.0.1/24", "fd94:685b:30cf:501::1/64"},
				},
			}, {
				Name: "vlan2",
				Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
					ID:              502,
					MTU:             1500,
					VNI:             4000003,
					VRF:             "m2m",
					AnycastMac:      "1a:ee:cf:2f:a7:a9",
					AnycastGateways: []string{"10.250.1.1/24", "fd94:685b:30cf:502::1/64"},
				},
			},
		},
		Vrf: []v1alpha1.VRFRevision{
			{
				Name: "m2m-test-vrf",
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF:         "m2m",
					Seq:         10,
					VNI:         types.ToPtr(2002026),
					RouteTarget: types.ToPtr("65188:2026"),
					Export: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{
							CIDR:   "10.250.0.0/24",
							LE:     types.ToPtr(32),
							Action: "permit",
						}, {
							CIDR:   "fd94:685b:30cf:501::/64",
							LE:     types.ToPtr(128),
							Action: "permit",
						}, {
							CIDR:   "10.250.1.0/24",
							LE:     types.ToPtr(32),
							Action: "permit",
						}, {
							CIDR:   "fd94:685b:30cf:502::/64",
							LE:     types.ToPtr(128),
							Action: "permit",
						}, {
							CIDR:   "10.100.0.0/24",
							LE:     types.ToPtr(32),
							Action: "permit",
						}, {
							CIDR:   "fdcb:f93c:3a3e::/64",
							LE:     types.ToPtr(128),
							Action: "permit",
						},
					},
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{
							CIDR:   "10.102.0.0/24",
							LE:     types.ToPtr(32),
							Action: "permit",
						},
						{
							CIDR:   "fda5:25c1:193c::/64",
							LE:     types.ToPtr(128),
							Action: "permit",
						},
					},
				},
			}, {
				Name: "internet",
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF:         "p_internet",
					Seq:         10,
					VNI:         types.ToPtr(2002001),
					RouteTarget: types.ToPtr("64512:2001"),
					Export: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{
							CIDR:   "192.0.2.0/29",
							LE:     types.ToPtr(32),
							Action: "permit",
						}, {
							CIDR:   "fd82:c2b0:431a::/64",
							LE:     types.ToPtr(128),
							Action: "permit",
						},
					},
				},
			},
		},
		BGP: []v1alpha1.BGPRevision{
			{
				Name: "bgppeering-test",
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					PeeringVlan: &v1alpha1.BGPPeeringVLAN{
						Name: "vlan1",
					},
					RemoteASN:       65511,
					MaximumPrefixes: types.ToPtr(uint32(5)),
					HoldTime:        &metav1.Duration{Duration: 3 * time.Second},
					KeepaliveTime:   &metav1.Duration{Duration: 1 * time.Second},
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{
							CIDR:   "100.64.1.0/24",
							LE:     types.ToPtr(32),
							Action: "permit",
						}, {
							CIDR:   "2a01:1::/64",
							LE:     types.ToPtr(128),
							Action: "permit",
						},
					},
				},
			},
		},
	},
}

var _ = BeforeSuite(func() {
	oldConfigPath, isConfigEnvExist = os.LookupEnv(operatorConfigEnv)
	os.Setenv(operatorConfigEnv, newConfigPath)

	file, err := os.Create(newConfigPath)
	Expect(err).ToNot(HaveOccurred())
	defer file.Close()

	content, _ := yaml.Marshal(config.Config{})
	_, err = file.Write(content)
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	if isConfigEnvExist {
		os.Setenv(operatorConfigEnv, oldConfigPath)
	} else {
		os.Unsetenv(operatorConfigEnv)
	}
	err := os.Remove(newConfigPath)
	Expect(err).ToNot(HaveOccurred())
})

var _ = Describe("CRA-VSR", func() {
	It("Find working NetNS", func() {
		ns, _ := manager.findWorkNS(manager.running)
		Expect(ns).To(Equal("hbn"))
	})
	It("Convert NodeNetworkConfig into VSR config", func() {
		scheme := runtime.NewScheme()
		utilruntime.Must(clientgoscheme.AddToScheme(scheme))
		utilruntime.Must(v1alpha1.AddToScheme(scheme))

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		reconciler, err := operator.NewNodeConfigReconciler(
			client, logr.Logger{}, 3, 3, 3, scheme, 3, operator.ImportModeImport)
		Expect(err).ToNot(HaveOccurred())

		node := &corev1.Node{}
		node.Name = "server1"

		nodeConfig, err := reconciler.CreateNodeNetworkConfig(node, revision)
		Expect(err).ToNot(HaveOccurred())

		nodeConfig.Spec.ClusterVRF.PolicyRoutes = []v1alpha1.PolicyRoute{
			{
				TrafficMatch: v1alpha1.TrafficMatch{
					SrcPrefix: types.ToPtr("1.1.1.0/24"),
					DstPrefix: types.ToPtr("2.2.2.3"),
				},
				NextHop: v1alpha1.NextHop{
					Vrf: types.ToPtr("mgmt"),
				},
			}, {
				TrafficMatch: v1alpha1.TrafficMatch{
					SrcPrefix: types.ToPtr("fd00:34::2"),
				},
				NextHop: v1alpha1.NextHop{
					Vrf: types.ToPtr("cluster"),
				},
			},
		}

		generated, err := manager.makeVRouter(&nodeConfig.Spec)
		Expect(err).ToNot(HaveOccurred())
		generated.Sort()
		generatedXML, err := xml.MarshalIndent(*generated, "", "  ")
		Expect(err).ToNot(HaveOccurred())

		// Could be uncommented to easily check the diff
		// f, _ := os.Create("/tmp/generated.xml")
		// f.Write(generatedXML)
		// f.Close()
		// f, _ = os.Create("/tmp/expected.xml")
		// f.Write([]byte(expectedXML))
		// f.Close()

		Expect(generatedXML).To(MatchXML(expectedXML))
	})
})

var expectedXML = `
<config xmlns="urn:6wind:vrouter" xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0">
  <vrf>
    <name>hbn</name>
    <routing xmlns="urn:6wind:vrouter/routing" nc:operation="replace">
      <policy-based-routing xmlns="urn:6wind:vrouter/pbr">
        <ipv4-rule>
          <priority>1</priority>
          <match>
            <inbound-interface>hbn</inbound-interface>
            <source>1.1.1.0/24</source>
            <destination>2.2.2.3</destination>
          </match>
          <action>
            <lookup>11</lookup>
          </action>
        </ipv4-rule>
        <ipv6-rule>
          <priority>2</priority>
          <match>
            <inbound-interface>hbn</inbound-interface>
            <source>fd00:34::2</source>
          </match>
          <action>
            <lookup>10</lookup>
          </action>
        </ipv6-rule>
      </policy-based-routing>
      <bgp xmlns="urn:6wind:vrouter/bgp">
        <as>64497</as>
        <router-id>10.50.0.10</router-id>
        <suppress-duplicates>false</suppress-duplicates>
        <ebgp-requires-policy>false</ebgp-requires-policy>
        <address-family>
          <ipv4-unicast>
            <network>
              <ip-prefix>10.50.0.10/32</ip-prefix>
            </network>
          </ipv4-unicast>
          <l2vpn-evpn>
            <advertise-all-vni>true</advertise-all-vni>
          </l2vpn-evpn>
        </address-family>
        <bestpath>
          <as-path>
            <multipath-relax>no-as-set</multipath-relax>
          </as-path>
        </bestpath>
        <neighbor-group>
          <name>ens3-leaf</name>
          <enforce-first-as>true</enforce-first-as>
          <remote-as>65500</remote-as>
          <local-as>
            <as-number>65501</as-number>
            <no-prepend>true</no-prepend>
            <replace-as>true</replace-as>
          </local-as>
          <timers>
            <keepalive-interval>30</keepalive-interval>
            <hold-time>90</hold-time>
          </timers>
          <address-family>
            <ipv4-unicast>
              <allowas-in>3</allowas-in>
              <route-map>
                <route-map-name>DENY-TAG-FABRIC-OUT</route-map-name>
                <route-direction>out</route-direction>
              </route-map>
              <route-map>
                <route-map-name>TAG-FABRIC-IN</route-map-name>
                <route-direction>in</route-direction>
              </route-map>
            </ipv4-unicast>
          </address-family>
          <track>bfd</track>
        </neighbor-group>
        <neighbor-group>
          <name>ens4-leaf</name>
          <enforce-first-as>true</enforce-first-as>
          <remote-as>65500</remote-as>
          <local-as>
            <as-number>65501</as-number>
            <no-prepend>true</no-prepend>
            <replace-as>true</replace-as>
          </local-as>
          <timers>
            <keepalive-interval>30</keepalive-interval>
            <hold-time>90</hold-time>
          </timers>
          <address-family>
            <ipv4-unicast>
              <allowas-in>3</allowas-in>
              <route-map>
                <route-map-name>DENY-TAG-FABRIC-OUT</route-map-name>
                <route-direction>out</route-direction>
              </route-map>
              <route-map>
                <route-map-name>TAG-FABRIC-IN</route-map-name>
                <route-direction>in</route-direction>
              </route-map>
            </ipv4-unicast>
          </address-family>
          <track>bfd</track>
        </neighbor-group>
        <neighbor>
          <neighbor-address>192.0.2.1</neighbor-address>
          <enforce-first-as>true</enforce-first-as>
          <remote-as>64497</remote-as>
          <timers>
            <keepalive-interval>30</keepalive-interval>
            <hold-time>90</hold-time>
          </timers>
          <address-family>
            <l2vpn-evpn>
              <route-map>
                <route-map-name>DENY-TAG-FABRIC-OUT</route-map-name>
                <route-direction>out</route-direction>
              </route-map>
              <route-map>
                <route-map-name>TAG-FABRIC-IN</route-map-name>
                <route-direction>in</route-direction>
              </route-map>
            </l2vpn-evpn>
          </address-family>
          <track>bfd</track>
        </neighbor>
        <neighbor>
          <neighbor-address>192.0.2.2</neighbor-address>
          <enforce-first-as>true</enforce-first-as>
          <remote-as>64497</remote-as>
          <timers>
            <keepalive-interval>30</keepalive-interval>
            <hold-time>90</hold-time>
          </timers>
          <address-family>
            <l2vpn-evpn>
              <route-map>
                <route-map-name>DENY-TAG-FABRIC-OUT</route-map-name>
                <route-direction>out</route-direction>
              </route-map>
              <route-map>
                <route-map-name>TAG-FABRIC-IN</route-map-name>
                <route-direction>in</route-direction>
              </route-map>
            </l2vpn-evpn>
          </address-family>
          <track>bfd</track>
        </neighbor>
        <unnumbered-neighbor>
          <interface>ens3</interface>
          <neighbor-group>ens3-leaf</neighbor-group>
          <ipv6-only>false</ipv6-only>
        </unnumbered-neighbor>
        <unnumbered-neighbor>
          <interface>ens4</interface>
          <neighbor-group>ens4-leaf</neighbor-group>
          <ipv6-only>false</ipv6-only>
        </unnumbered-neighbor>
      </bgp>
    </routing>
    <interface xmlns="urn:6wind:vrouter/interface">
      <vxlan xmlns="urn:6wind:vrouter/vxlan">
        <name>vx.4000002</name>
        <vni>4000002</vni>
        <mtu>1500</mtu>
        <dst>4789</dst>
        <local>10.50.0.10</local>
        <learning>false</learning>
        <ethernet>
          <mac-address>02:54:0a:32:00:0a</mac-address>
        </ethernet>
        <network-stack>
          <ipv6>
            <address-generation-mode>no-link-local</address-generation-mode>
          </ipv6>
        </network-stack>
        <link-interface>dum.underlay</link-interface>
      </vxlan>
      <vxlan xmlns="urn:6wind:vrouter/vxlan">
        <name>vx.4000003</name>
        <vni>4000003</vni>
        <mtu>1500</mtu>
        <dst>4789</dst>
        <local>10.50.0.10</local>
        <learning>false</learning>
        <ethernet>
          <mac-address>02:54:0a:32:00:0a</mac-address>
        </ethernet>
        <network-stack>
          <ipv6>
            <address-generation-mode>no-link-local</address-generation-mode>
          </ipv6>
        </network-stack>
        <link-interface>dum.underlay</link-interface>
      </vxlan>
      <vxlan xmlns="urn:6wind:vrouter/vxlan">
        <name>vx.m2m</name>
        <vni>2002026</vni>
        <mtu>9000</mtu>
        <dst>4789</dst>
        <local>10.50.0.10</local>
        <learning>false</learning>
        <ethernet>
          <mac-address>02:54:0a:32:00:0a</mac-address>
        </ethernet>
        <network-stack>
          <ipv6>
            <address-generation-mode>no-link-local</address-generation-mode>
          </ipv6>
        </network-stack>
        <link-interface>dum.underlay</link-interface>
      </vxlan>
      <vxlan xmlns="urn:6wind:vrouter/vxlan">
        <name>vx.p_internet</name>
        <vni>2002001</vni>
        <mtu>9000</mtu>
        <dst>4789</dst>
        <local>10.50.0.10</local>
        <learning>false</learning>
        <ethernet>
          <mac-address>02:54:0a:32:00:0a</mac-address>
        </ethernet>
        <network-stack>
          <ipv6>
            <address-generation-mode>no-link-local</address-generation-mode>
          </ipv6>
        </network-stack>
        <link-interface>dum.underlay</link-interface>
      </vxlan>
      <vlan xmlns="urn:6wind:vrouter/vlan">
        <name>vlan.501</name>
        <vlan-id>501</vlan-id>
        <link-interface>hbn</link-interface>
        <mtu>1500</mtu>
        <network-stack>
          <ipv6>
            <address-generation-mode>no-link-local</address-generation-mode>
          </ipv6>
        </network-stack>
      </vlan>
      <vlan xmlns="urn:6wind:vrouter/vlan">
        <name>vlan.502</name>
        <vlan-id>502</vlan-id>
        <link-interface>hbn</link-interface>
        <mtu>1500</mtu>
        <network-stack>
          <ipv6>
            <address-generation-mode>no-link-local</address-generation-mode>
          </ipv6>
        </network-stack>
      </vlan>
    </interface>
    <l3vrf>
      <name>cluster</name>
      <table-id>10</table-id>
      <routing xmlns="urn:6wind:vrouter/routing" nc:operation="replace">
        <static>
          <ipv4-route>
            <destination>10.100.0.0/24</destination>
            <next-hop>
              <next-hop>blackhole</next-hop>
            </next-hop>
          </ipv4-route>
          <ipv4-route>
            <destination>10.100.0.10/32</destination>
            <next-hop>
              <next-hop>hbn</next-hop>
            </next-hop>
          </ipv4-route>
          <ipv6-route>
            <destination>fd00:7:caa5::1/128</destination>
            <next-hop>
              <next-hop>hbn</next-hop>
            </next-hop>
          </ipv6-route>
          <ipv6-route>
            <destination>fdcb:f93c:3a3e::/64</destination>
            <next-hop>
              <next-hop>blackhole</next-hop>
            </next-hop>
          </ipv6-route>
          <ipv6-route>
            <destination>fdcb:f93c:3a3e::10/128</destination>
            <next-hop>
              <next-hop>hbn</next-hop>
            </next-hop>
          </ipv6-route>
        </static>
        <bgp xmlns="urn:6wind:vrouter/bgp">
          <as>64497</as>
          <router-id>10.50.0.10</router-id>
          <suppress-duplicates>false</suppress-duplicates>
          <l3vni>30</l3vni>
          <address-family>
            <ipv4-unicast>
              <redistribute>
                <protocol>connected</protocol>
              </redistribute>
              <redistribute>
                <protocol>kernel</protocol>
              </redistribute>
              <redistribute>
                <protocol>static</protocol>
              </redistribute>
              <l3vrf>
                <import>
                  <l3vrf>m2m</l3vrf>
                  <l3vrf>mgmt</l3vrf>
                  <route-map>rm_cluster_import</route-map>
                </import>
              </l3vrf>
            </ipv4-unicast>
            <ipv6-unicast>
              <redistribute>
                <protocol>connected</protocol>
              </redistribute>
              <redistribute>
                <protocol>kernel</protocol>
              </redistribute>
              <redistribute>
                <protocol>static</protocol>
              </redistribute>
              <l3vrf>
                <import>
                  <l3vrf>m2m</l3vrf>
                  <l3vrf>mgmt</l3vrf>
                  <route-map>rm_cluster_import</route-map>
                </import>
              </l3vrf>
            </ipv6-unicast>
            <l2vpn-evpn>
              <advertisement>
                <ipv4-unicast>
                  <route-map>rm_export_local</route-map>
                </ipv4-unicast>
                <ipv6-unicast>
                  <route-map>rm_export_local</route-map>
                </ipv6-unicast>
              </advertisement>
              <export>
                <route-target>64497:30</route-target>
              </export>
              <import>
                <route-target>64497:30</route-target>
              </import>
            </l2vpn-evpn>
          </address-family>
          <neighbor>
            <neighbor-address>10.100.0.10</neighbor-address>
            <enforce-first-as>true</enforce-first-as>
            <remote-as>65170</remote-as>
            <local-as>
              <as-number>65169</as-number>
              <no-prepend>true</no-prepend>
              <replace-as>true</replace-as>
            </local-as>
            <timers>
              <keepalive-interval>30</keepalive-interval>
              <hold-time>90</hold-time>
            </timers>
            <address-family>
              <ipv4-unicast>
                <allowas-in>3</allowas-in>
                <prefix-list>
                  <prefix-list-name>ANY</prefix-list-name>
                  <update-direction>in</update-direction>
                </prefix-list>
              </ipv4-unicast>
            </address-family>
            <update-source>169.254.100.100</update-source>
            <enforce-multihop>true</enforce-multihop>
          </neighbor>
          <neighbor>
            <neighbor-address>fd00:7:caa5::1</neighbor-address>
            <enforce-first-as>true</enforce-first-as>
            <remote-as>65170</remote-as>
            <local-as>
              <as-number>65169</as-number>
              <no-prepend>true</no-prepend>
              <replace-as>true</replace-as>
            </local-as>
            <timers>
              <keepalive-interval>30</keepalive-interval>
              <hold-time>90</hold-time>
            </timers>
            <address-family>
              <ipv4-unicast>
                <allowas-in>3</allowas-in>
                <prefix-list>
                  <prefix-list-name>ANY</prefix-list-name>
                  <update-direction>in</update-direction>
                </prefix-list>
              </ipv4-unicast>
              <ipv6-unicast>
                <prefix-list>
                  <prefix-list-name>ANY</prefix-list-name>
                  <update-direction>in</update-direction>
                </prefix-list>
              </ipv6-unicast>
            </address-family>
          </neighbor>
          <neighbor>
            <neighbor-address>fdcb:f93c:3a3e::10</neighbor-address>
            <enforce-first-as>true</enforce-first-as>
            <remote-as>65170</remote-as>
            <local-as>
              <as-number>65169</as-number>
              <no-prepend>true</no-prepend>
              <replace-as>true</replace-as>
            </local-as>
            <timers>
              <keepalive-interval>30</keepalive-interval>
              <hold-time>90</hold-time>
            </timers>
            <address-family>
              <ipv6-unicast>
                <prefix-list>
                  <prefix-list-name>ANY</prefix-list-name>
                  <update-direction>in</update-direction>
                </prefix-list>
              </ipv6-unicast>
            </address-family>
            <update-source>fd00:7:caa5:1::</update-source>
            <enforce-multihop>true</enforce-multihop>
          </neighbor>
        </bgp>
      </routing>
      <interface xmlns="urn:6wind:vrouter/interface"></interface>
    </l3vrf>
    <l3vrf>
      <name>m2m</name>
      <table-id>50</table-id>
      <routing xmlns="urn:6wind:vrouter/routing" nc:operation="replace">
        <static></static>
        <bgp xmlns="urn:6wind:vrouter/bgp">
          <as>64497</as>
          <router-id>10.50.0.10</router-id>
          <suppress-duplicates>false</suppress-duplicates>
          <l3vni>2002026</l3vni>
          <listen>
            <neighbor-range>
              <address>10.250.0.0/24</address>
              <neighbor-group>bf90eaf1</neighbor-group>
            </neighbor-range>
            <neighbor-range>
              <address>fd94:685b:30cf:501::/64</address>
              <neighbor-group>a3a5b583</neighbor-group>
            </neighbor-range>
          </listen>
          <address-family>
            <ipv4-unicast>
              <redistribute>
                <protocol>connected</protocol>
              </redistribute>
              <redistribute>
                <protocol>kernel</protocol>
              </redistribute>
              <redistribute>
                <protocol>static</protocol>
              </redistribute>
              <l3vrf>
                <import>
                  <l3vrf>cluster</l3vrf>
                  <route-map>rm_m2m_import</route-map>
                </import>
              </l3vrf>
            </ipv4-unicast>
            <ipv6-unicast>
              <redistribute>
                <protocol>connected</protocol>
              </redistribute>
              <redistribute>
                <protocol>kernel</protocol>
              </redistribute>
              <redistribute>
                <protocol>static</protocol>
              </redistribute>
              <l3vrf>
                <import>
                  <l3vrf>cluster</l3vrf>
                  <route-map>rm_m2m_import</route-map>
                </import>
              </l3vrf>
            </ipv6-unicast>
            <l2vpn-evpn>
              <advertisement>
                <ipv4-unicast>
                  <route-map>rm_m2m_export</route-map>
                </ipv4-unicast>
                <ipv6-unicast>
                  <route-map>rm_m2m_export</route-map>
                </ipv6-unicast>
              </advertisement>
              <export>
                <route-target>65188:2026</route-target>
              </export>
              <import>
                <route-target>65188:2026</route-target>
              </import>
            </l2vpn-evpn>
          </address-family>
          <neighbor-group>
            <name>a3a5b583</name>
            <enforce-first-as>true</enforce-first-as>
            <remote-as>65511</remote-as>
            <timers>
              <keepalive-interval>1</keepalive-interval>
              <hold-time>3</hold-time>
            </timers>
            <address-family>
              <ipv6-unicast>
                <route-map>
                  <route-map-name>rm_a3a5b583-ipv6-in</route-map-name>
                  <route-direction>in</route-direction>
                </route-map>
                <route-map>
                  <route-map-name>rm_a3a5b583-ipv6-out</route-map-name>
                  <route-direction>out</route-direction>
                </route-map>
                <maximum-prefix>
                  <maximum>5</maximum>
                </maximum-prefix>
              </ipv6-unicast>
            </address-family>
          </neighbor-group>
          <neighbor-group>
            <name>bf90eaf1</name>
            <enforce-first-as>true</enforce-first-as>
            <remote-as>65511</remote-as>
            <timers>
              <keepalive-interval>1</keepalive-interval>
              <hold-time>3</hold-time>
            </timers>
            <address-family>
              <ipv4-unicast>
                <route-map>
                  <route-map-name>rm_bf90eaf1-ipv4-in</route-map-name>
                  <route-direction>in</route-direction>
                </route-map>
                <route-map>
                  <route-map-name>rm_bf90eaf1-ipv4-out</route-map-name>
                  <route-direction>out</route-direction>
                </route-map>
                <maximum-prefix>
                  <maximum>5</maximum>
                </maximum-prefix>
              </ipv4-unicast>
            </address-family>
          </neighbor-group>
        </bgp>
      </routing>
      <interface xmlns="urn:6wind:vrouter/interface">
        <bridge xmlns="urn:6wind:vrouter/bridge">
          <name>br.m2m</name>
          <mtu>9000</mtu>
          <ethernet>
            <mac-address>02:54:0a:32:00:0a</mac-address>
          </ethernet>
          <link-interface>
            <slave>vx.m2m</slave>
            <learning>false</learning>
            <neighbor-suppress>false</neighbor-suppress>
            <hairpin>true</hairpin>
          </link-interface>
          <network-stack>
            <ipv6>
              <address-generation-mode>no-link-local</address-generation-mode>
            </ipv6>
          </network-stack>
        </bridge>
        <bridge xmlns="urn:6wind:vrouter/bridge">
          <name>l2.501</name>
          <mtu>1500</mtu>
          <ethernet>
            <mac-address>1a:ee:cf:2f:a7:a8</mac-address>
          </ethernet>
          <link-interface>
            <slave>vlan.501</slave>
          </link-interface>
          <link-interface>
            <slave>vx.4000002</slave>
            <learning>false</learning>
            <neighbor-suppress>false</neighbor-suppress>
            <hairpin>false</hairpin>
          </link-interface>
          <ipv4>
            <address>
              <ip>10.250.0.1/24</ip>
            </address>
          </ipv4>
          <ipv6>
            <address>
              <ip>fd94:685b:30cf:501::1/64</ip>
            </address>
          </ipv6>
          <network-stack>
            <ipv4>
              <arp-accept-gratuitous>always</arp-accept-gratuitous>
            </ipv4>
            <neighbor>
              <ipv4-base-reachable-time>30000</ipv4-base-reachable-time>
              <ipv6-base-reachable-time>30000</ipv6-base-reachable-time>
            </neighbor>
          </network-stack>
        </bridge>
        <bridge xmlns="urn:6wind:vrouter/bridge">
          <name>l2.502</name>
          <mtu>1500</mtu>
          <ethernet>
            <mac-address>1a:ee:cf:2f:a7:a9</mac-address>
          </ethernet>
          <link-interface>
            <slave>vlan.502</slave>
          </link-interface>
          <link-interface>
            <slave>vx.4000003</slave>
            <learning>false</learning>
            <neighbor-suppress>false</neighbor-suppress>
            <hairpin>false</hairpin>
          </link-interface>
          <ipv4>
            <address>
              <ip>10.250.1.1/24</ip>
            </address>
          </ipv4>
          <ipv6>
            <address>
              <ip>fd94:685b:30cf:502::1/64</ip>
            </address>
          </ipv6>
          <network-stack>
            <ipv4>
              <arp-accept-gratuitous>always</arp-accept-gratuitous>
            </ipv4>
            <neighbor>
              <ipv4-base-reachable-time>30000</ipv4-base-reachable-time>
              <ipv6-base-reachable-time>30000</ipv6-base-reachable-time>
            </neighbor>
          </network-stack>
        </bridge>
      </interface>
    </l3vrf>
    <l3vrf>
      <name>mgmt</name>
      <table-id>11</table-id>
      <routing xmlns="urn:6wind:vrouter/routing" nc:operation="replace">
        <static></static>
        <bgp xmlns="urn:6wind:vrouter/bgp">
          <as>64497</as>
          <router-id>10.50.0.10</router-id>
          <suppress-duplicates>false</suppress-duplicates>
          <l3vni>20</l3vni>
          <address-family>
            <ipv4-unicast>
              <redistribute>
                <protocol>connected</protocol>
              </redistribute>
              <redistribute>
                <protocol>static</protocol>
              </redistribute>
              <l3vrf>
                <import>
                  <l3vrf>cluster</l3vrf>
                  <route-map>rm_mgmt_import</route-map>
                </import>
              </l3vrf>
            </ipv4-unicast>
            <ipv6-unicast>
              <redistribute>
                <protocol>connected</protocol>
              </redistribute>
              <redistribute>
                <protocol>static</protocol>
              </redistribute>
              <l3vrf>
                <import>
                  <l3vrf>cluster</l3vrf>
                  <route-map>rm_mgmt_import</route-map>
                </import>
              </l3vrf>
            </ipv6-unicast>
            <l2vpn-evpn>
              <advertisement>
                <ipv4-unicast>
                  <route-map>rm_export_local</route-map>
                </ipv4-unicast>
                <ipv6-unicast>
                  <route-map>rm_export_local</route-map>
                </ipv6-unicast>
              </advertisement>
              <export>
                <route-target>64497:20</route-target>
              </export>
              <import>
                <route-target>64497:20</route-target>
              </import>
            </l2vpn-evpn>
          </address-family>
        </bgp>
      </routing>
      <interface xmlns="urn:6wind:vrouter/interface"></interface>
    </l3vrf>
    <l3vrf>
      <name>p_internet</name>
      <table-id>51</table-id>
      <routing xmlns="urn:6wind:vrouter/routing" nc:operation="replace">
        <static></static>
        <bgp xmlns="urn:6wind:vrouter/bgp">
          <as>64497</as>
          <router-id>10.50.0.10</router-id>
          <suppress-duplicates>false</suppress-duplicates>
          <l3vni>2002001</l3vni>
          <address-family>
            <ipv4-unicast>
              <redistribute>
                <protocol>connected</protocol>
              </redistribute>
              <redistribute>
                <protocol>kernel</protocol>
              </redistribute>
              <redistribute>
                <protocol>static</protocol>
              </redistribute>
              <l3vrf>
                <import>
                  <l3vrf>cluster</l3vrf>
                  <route-map>rm_p_internet_import</route-map>
                </import>
              </l3vrf>
            </ipv4-unicast>
            <ipv6-unicast>
              <redistribute>
                <protocol>connected</protocol>
              </redistribute>
              <redistribute>
                <protocol>kernel</protocol>
              </redistribute>
              <redistribute>
                <protocol>static</protocol>
              </redistribute>
              <l3vrf>
                <import>
                  <l3vrf>cluster</l3vrf>
                  <route-map>rm_p_internet_import</route-map>
                </import>
              </l3vrf>
            </ipv6-unicast>
            <l2vpn-evpn>
              <advertisement>
                <ipv4-unicast>
                  <route-map>rm_p_internet_export</route-map>
                </ipv4-unicast>
                <ipv6-unicast>
                  <route-map>rm_p_internet_export</route-map>
                </ipv6-unicast>
              </advertisement>
              <export>
                <route-target>64512:2001</route-target>
              </export>
              <import>
	        <route-target>64512:2001</route-target>
              </import>
            </l2vpn-evpn>
          </address-family>
        </bgp>
      </routing>
      <interface xmlns="urn:6wind:vrouter/interface">
        <bridge xmlns="urn:6wind:vrouter/bridge">
          <name>br.p_internet</name>
          <mtu>9000</mtu>
          <ethernet>
            <mac-address>02:54:0a:32:00:0a</mac-address>
          </ethernet>
          <link-interface>
            <slave>vx.p_internet</slave>
            <learning>false</learning>
            <neighbor-suppress>false</neighbor-suppress>
            <hairpin>true</hairpin>
          </link-interface>
          <network-stack>
            <ipv6>
              <address-generation-mode>no-link-local</address-generation-mode>
            </ipv6>
          </network-stack>
        </bridge>
      </interface>
    </l3vrf>
  </vrf>
  <routing xmlns="urn:6wind:vrouter/routing" nc:operation="replace">
    <route-map>
      <name>DENY-TAG-FABRIC-OUT</name>
      <seq>
        <num>10</num>
        <policy>deny</policy>
        <match>
          <community xmlns="urn:6wind:vrouter/bgp">
            <id>cm-received-fabric</id>
          </community>
        </match>
      </seq>
      <seq>
        <num>20</num>
        <policy>permit</policy>
      </seq>
    </route-map>
    <route-map>
      <name>TAG-FABRIC-IN</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <set>
          <local-preference>100</local-preference>
          <community xmlns="urn:6wind:vrouter/bgp">
            <add>
              <attribute>65169:200</attribute>
            </add>
          </community>
        </set>
      </seq>
    </route-map>
    <route-map>
      <name>rm_a3a5b583-ipv6-in</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_a3a5b583-ipv6-in_0</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>11</num>
        <policy>deny</policy>
      </seq>
    </route-map>
    <route-map>
      <name>rm_a3a5b583-ipv6-out</name>
      <seq>
        <num>10</num>
        <policy>deny</policy>
      </seq>
    </route-map>
    <route-map>
      <name>rm_bf90eaf1-ipv4-in</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_bf90eaf1-ipv4-in_0</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>11</num>
        <policy>deny</policy>
      </seq>
    </route-map>
    <route-map>
      <name>rm_bf90eaf1-ipv4-out</name>
      <seq>
        <num>10</num>
        <policy>deny</policy>
      </seq>
    </route-map>
    <route-map>
      <name>rm_cluster_import</name>
      <seq>
        <num>1</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>ANY</prefix-list>
            </address>
          </ip>
        </match>
        <set>
          <ipv4>
            <vpn>
              <next-hop>0.0.0.0</next-hop>
            </vpn>
          </ipv4>
        </set>
        <on-match>next</on-match>
      </seq>
      <seq>
        <num>2</num>
        <policy>permit</policy>
        <set>
          <local-preference>50</local-preference>
        </set>
        <on-match>next</on-match>
      </seq>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <source-l3vrf>m2m</source-l3vrf>
        </match>
        <call>rm_cluster_import_m2m</call>
      </seq>
      <seq>
        <num>65533</num>
        <policy>deny</policy>
        <match>
          <ip>
            <address>
              <prefix-list>DEFAULT</prefix-list>
            </address>
          </ip>
          <source-l3vrf>mgmt</source-l3vrf>
        </match>
      </seq>
      <seq>
        <num>65534</num>
        <policy>deny</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>DEFAULT</prefix-list>
            </address>
          </ipv6>
          <source-l3vrf>mgmt</source-l3vrf>
        </match>
      </seq>
      <seq>
        <num>65535</num>
        <policy>permit</policy>
        <match>
          <source-l3vrf>mgmt</source-l3vrf>
        </match>
      </seq>
    </route-map>
    <route-map>
      <name>rm_cluster_import_m2m</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_cluster_import_m2m_0</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>11</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_cluster_import_m2m_1</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>12</num>
        <policy>deny</policy>
      </seq>
    </route-map>
    <route-map>
      <name>rm_export_local</name>
      <seq>
        <num>10</num>
        <policy>deny</policy>
        <match>
          <community xmlns="urn:6wind:vrouter/bgp">
            <id>cm-received-fabric</id>
          </community>
        </match>
      </seq>
      <seq>
        <num>11</num>
        <policy>deny</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_link_local</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>12</num>
        <policy>deny</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_link_local</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>20</num>
        <policy>permit</policy>
      </seq>
    </route-map>
    <route-map>
      <name>rm_m2m_export</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_m2m_export_0</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>11</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_m2m_export_1</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>12</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_m2m_export_2</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>13</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_m2m_export_3</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>14</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_m2m_export_4</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>15</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_m2m_export_5</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>16</num>
        <policy>deny</policy>
      </seq>
    </route-map>
    <route-map>
      <name>rm_m2m_import</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <source-l3vrf>cluster</source-l3vrf>
        </match>
        <call>rm_m2m_import_cluster</call>
      </seq>
    </route-map>
    <route-map>
      <name>rm_m2m_import_cluster</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_m2m_import_cluster_0</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>11</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_m2m_import_cluster_1</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>12</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_m2m_import_cluster_2</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>13</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_m2m_import_cluster_3</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>14</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_m2m_import_cluster_4</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>15</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_m2m_import_cluster_5</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>16</num>
        <policy>deny</policy>
      </seq>
    </route-map>
    <route-map>
      <name>rm_mgmt_import</name>
      <seq>
        <num>2</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_export_base</prefix-list>
            </address>
          </ip>
          <source-l3vrf>cluster</source-l3vrf>
        </match>
      </seq>
      <seq>
        <num>3</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_export_base</prefix-list>
            </address>
          </ipv6>
          <source-l3vrf>cluster</source-l3vrf>
        </match>
      </seq>
    </route-map>
    <route-map>
      <name>rm_p_internet_export</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_p_internet_export_0</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>11</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_p_internet_export_1</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>12</num>
        <policy>deny</policy>
      </seq>
    </route-map>
    <route-map>
      <name>rm_p_internet_import</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <source-l3vrf>cluster</source-l3vrf>
        </match>
        <call>rm_p_internet_import_cluster</call>
      </seq>
    </route-map>
    <route-map>
      <name>rm_p_internet_import_cluster</name>
      <seq>
        <num>10</num>
        <policy>permit</policy>
        <match>
          <ip>
            <address>
              <prefix-list>pl_p_internet_import_cluster_0</prefix-list>
            </address>
          </ip>
        </match>
      </seq>
      <seq>
        <num>11</num>
        <policy>permit</policy>
        <match>
          <ipv6>
            <address>
              <prefix-list>pl_p_internet_import_cluster_1</prefix-list>
            </address>
          </ipv6>
        </match>
      </seq>
      <seq>
        <num>12</num>
        <policy>deny</policy>
      </seq>
    </route-map>
    <ipv4-prefix-list>
      <name>ANY</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>DEFAULT</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>0.0.0.0/0</address>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_bf90eaf1-ipv4-in_0</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>100.64.1.0/24</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_cluster_import_m2m_0</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>10.102.0.0/24</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_export_base</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>10.100.0.0/24</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_link_local</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>169.254.0.0/16</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_m2m_export_0</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>10.250.0.0/24</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_m2m_export_2</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>10.250.1.0/24</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_m2m_export_4</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>10.100.0.0/24</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_m2m_import_cluster_0</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>10.250.0.0/24</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_m2m_import_cluster_2</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>10.250.1.0/24</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_m2m_import_cluster_4</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>10.100.0.0/24</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_p_internet_export_0</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>192.0.2.0/29</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv4-prefix-list>
      <name>pl_p_internet_import_cluster_0</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>192.0.2.0/29</address>
        <le>32</le>
      </seq>
    </ipv4-prefix-list>
    <ipv6-prefix-list>
      <name>ANY</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>DEFAULT</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>::/0</address>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_a3a5b583-ipv6-in_0</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>2a01:1::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_cluster_import_m2m_1</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fda5:25c1:193c::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_export_base</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fdcb:f93c:3a3e::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_link_local</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fd00:7:caa5::/48</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_m2m_export_1</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fd94:685b:30cf:501::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_m2m_export_3</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fd94:685b:30cf:502::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_m2m_export_5</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fdcb:f93c:3a3e::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_m2m_import_cluster_1</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fd94:685b:30cf:501::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_m2m_import_cluster_3</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fd94:685b:30cf:502::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_m2m_import_cluster_5</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fdcb:f93c:3a3e::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_p_internet_export_1</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fd82:c2b0:431a::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <ipv6-prefix-list>
      <name>pl_p_internet_import_cluster_1</name>
      <seq>
        <num>5</num>
        <policy>permit</policy>
        <address>fd82:c2b0:431a::/64</address>
        <le>128</le>
      </seq>
    </ipv6-prefix-list>
    <bgp xmlns="urn:6wind:vrouter/bgp">
      <community-list>
        <name>cm-received-fabric</name>
        <policy>
          <priority>5</priority>
          <policy>permit</policy>
          <community>65169:200</community>
        </policy>
      </community-list>
    </bgp>
  </routing>
</config>`
