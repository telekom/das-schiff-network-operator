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
	WorkNS: "hbn",
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
		generatedXML, err := xml.MarshalIndent(
			VRouterConfig{VRouter: *generated}, "", "  ")
		Expect(err).ToNot(HaveOccurred())

		expectedXML, err := os.ReadFile("cra_vsr_test.xml")
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
