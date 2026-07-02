package operator

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

const (
	testRouteTarget      = "65000:1000"
	testConfigTimeout    = 5 * time.Minute
	testPreconfigTimeout = 10 * time.Minute
)

// newTestCRR creates a ConfigRevisionReconciler with an inline test config.
func newTestCRR(importMode ImportMode) *ConfigRevisionReconciler {
	cfg := &config.Config{
		VRFConfig: map[string]config.VRFConfig{
			"tenant-a": {VNI: 1000, RT: testRouteTarget},
			"tenant-b": {VNI: 2000, RT: "65000:2000"},
		},
		VRFToVNI: map[string]int{
			"tenant-c": 3000,
		},
	}
	return &ConfigRevisionReconciler{
		logger:           ctrl.Log.WithName("test"),
		vrfConfig:        cfg,
		configTimeout:    testConfigTimeout,
		preconfigTimeout: testPreconfigTimeout,
		importMode:       importMode,
	}
}

var _ = Describe("VRF building", func() {
	Describe("createDefaultClusterVRF", func() {
		It("should return VRF with Static and Connected redistribution", func() {
			vrf := createDefaultClusterVRF()
			Expect(vrf).ToNot(BeNil())
			Expect(vrf.Redistribute).ToNot(BeNil())
			Expect(vrf.Redistribute.Static).ToNot(BeNil())
			Expect(vrf.Redistribute.Connected).ToNot(BeNil())
			Expect(vrf.Redistribute.Static.DefaultAction.Type).To(Equal(v1alpha1.Accept))
			Expect(vrf.Redistribute.Connected.DefaultAction.Type).To(Equal(v1alpha1.Accept))
		})
	})

	Describe("buildNodeVrf", func() {
		It("should skip VRFs that do not match node selector", func() {
			crr := newTestCRR(ImportModeImport)
			node := makeNode("node1", true)
			node.Labels = map[string]string{"role": "worker"}

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Vrf: []v1alpha1.VRFRevision{
						{
							Name: "vrf-a",
							VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
								VRF:    "tenant-a",
								Seq:    10,
								Import: []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Export: []v1alpha1.VrfRouteConfigurationPrefixItem{},
								NodeSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"role": "master"},
								},
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := crr.buildNodeVrf(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.FabricVRFs).To(BeEmpty())
		})

		It("should create fabric VRF for matching node selector", func() {
			crr := newTestCRR(ImportModeImport)
			node := makeNode("node1", true)
			node.Labels = map[string]string{"role": "worker"}

			vni := 1000
			rt := testRouteTarget
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Vrf: []v1alpha1.VRFRevision{
						{
							Name: "vrf-a",
							VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
								VRF:         "tenant-a",
								Seq:         10,
								VNI:         &vni,
								RouteTarget: &rt,
								Import:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Export:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
								NodeSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"role": "worker"},
								},
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := crr.buildNodeVrf(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.FabricVRFs).To(HaveKey("tenant-a"))
			fabricVRF := c.Spec.FabricVRFs["tenant-a"]
			Expect(fabricVRF.VNI).To(Equal(uint32(1000)))
		})

		It("should create local VRF with SBR when SBRPrefixes are set", func() {
			crr := newTestCRR(ImportModeImport)
			node := makeNode("node1", true)

			vni := 1000
			rt := testRouteTarget
			sbrPrefix := "10.10.0.0/24"
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Vrf: []v1alpha1.VRFRevision{
						{
							Name: "vrf-a",
							VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
								VRF:         "tenant-a",
								Seq:         10,
								VNI:         &vni,
								RouteTarget: &rt,
								SBRPrefixes: []string{sbrPrefix},
								Import:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Export:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := crr.buildNodeVrf(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.LocalVRFs).To(HaveKey("s-tenant-a"))
			Expect(c.Spec.ClusterVRF.PolicyRoutes).To(HaveLen(1))
		})

		It("should return error when VNI/RT not found in config and not in spec", func() {
			cfg := &config.Config{
				VRFConfig: map[string]config.VRFConfig{},
				VRFToVNI:  map[string]int{},
			}
			crr := &ConfigRevisionReconciler{
				logger:     ctrl.Log.WithName("test"),
				vrfConfig:  cfg,
				importMode: ImportModeImport,
			}
			node := makeNode("node1", true)

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Vrf: []v1alpha1.VRFRevision{
						{
							Name: "vrf-a",
							VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
								VRF:    "unknown-vrf",
								Seq:    10,
								Import: []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Export: []v1alpha1.VrfRouteConfigurationPrefixItem{},
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := crr.buildNodeVrf(node, revision, c)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error getting VNI and RT for VRF unknown-vrf"))
		})
	})

	Describe("updateFabricVRF", func() {
		It("should add aggregate routes as static routes", func() {
			fabricVrf := v1alpha1.FabricVRF{
				VRF: v1alpha1.VRF{
					VRFImports: []v1alpha1.VRFImport{
						{
							FromVRF: "cluster",
							Filter: v1alpha1.Filter{
								DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
							},
						},
					},
				},
				EVPNExportFilter: &v1alpha1.Filter{
					DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
				},
			}
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF:       "tenant-a",
					Aggregate: []string{"10.0.0.0/8", "192.168.0.0/16"},
					Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			importMap := make(map[string]v1alpha1.VRFImport)

			updateFabricVRF(&fabricVrf, vrf, importMap, ImportModeImport)
			Expect(fabricVrf.StaticRoutes).To(HaveLen(2))
			Expect(fabricVrf.StaticRoutes[0].Prefix).To(Equal("10.0.0.0/8"))
			Expect(fabricVrf.StaticRoutes[1].Prefix).To(Equal("192.168.0.0/16"))
		})

		It("should not process imports when importMode is not ImportModeImport", func() {
			fabricVrf := v1alpha1.FabricVRF{
				VRF: v1alpha1.VRF{
					VRFImports: []v1alpha1.VRFImport{
						{
							FromVRF: "cluster",
							Filter: v1alpha1.Filter{
								DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
							},
						},
					},
				},
				EVPNExportFilter: &v1alpha1.Filter{
					DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
				},
			}
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF: "tenant-a",
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit"},
					},
					Export: []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			importMap := make(map[string]v1alpha1.VRFImport)

			updateFabricVRF(&fabricVrf, vrf, importMap, ImportModeStaticRoute)
			// importMap should not be populated when mode is StaticRoute
			Expect(importMap).To(BeEmpty())
		})
	})

	Describe("processExports", func() {
		It("should build filter items from export entries", func() {
			fabricVrf := v1alpha1.FabricVRF{
				VRF: v1alpha1.VRF{
					VRFImports: []v1alpha1.VRFImport{
						{
							FromVRF: "cluster",
							Filter: v1alpha1.Filter{
								DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
							},
						},
					},
				},
				EVPNExportFilter: &v1alpha1.Filter{
					DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
				},
			}
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					Export: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit"},
						{CIDR: "192.168.0.0/16", Seq: 20, Action: "deny"},
					},
				},
			}

			processExports(vrf, &fabricVrf)
			Expect(fabricVrf.EVPNExportFilter.Items).To(HaveLen(2))
			Expect(fabricVrf.EVPNExportFilter.Items[0].Action.Type).To(Equal(v1alpha1.Accept))
			Expect(fabricVrf.EVPNExportFilter.Items[1].Action.Type).To(Equal(v1alpha1.Reject))
		})

		It("should add community to VRF import when community is set", func() {
			community := "65000:1"
			fabricVrf := v1alpha1.FabricVRF{
				VRF: v1alpha1.VRF{
					VRFImports: []v1alpha1.VRFImport{
						{
							FromVRF: "cluster",
							Filter: v1alpha1.Filter{
								DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
							},
						},
					},
				},
				EVPNExportFilter: &v1alpha1.Filter{
					DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
				},
			}
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					Community: &community,
					Export: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit"},
					},
				},
			}

			processExports(vrf, &fabricVrf)
			Expect(fabricVrf.VRFImports[0].Filter.Items[0].Action.ModifyRoute).ToNot(BeNil())
			Expect(fabricVrf.VRFImports[0].Filter.Items[0].Action.ModifyRoute.AddCommunities).To(ContainElement("65000:1"))
		})
	})

	Describe("processImports", func() {
		It("should populate defaultImportMap with import entries", func() {
			importMap := make(map[string]v1alpha1.VRFImport)
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF: "tenant-a",
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit"},
						{CIDR: "172.16.0.0/12", Seq: 20, Action: "deny"},
					},
				},
			}

			processImports(vrf, importMap)
			Expect(importMap).To(HaveKey("tenant-a"))
			entry := importMap["tenant-a"]
			Expect(entry.FromVRF).To(Equal("tenant-a"))
			Expect(entry.Filter.Items).To(HaveLen(2))
		})
	})

	Describe("updateLocalVRFs", func() {
		It("should create SBR intermediate VRF and policy routes when SBRPrefixes set", func() {
			localVRFs := make(map[string]v1alpha1.VRF)
			clusterVRF := createDefaultClusterVRF()
			sbrPrefix1 := "10.10.0.0/24"
			sbrPrefix2 := "10.20.0.0/24"
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF:         "tenant-a",
					SBRPrefixes: []string{sbrPrefix1, sbrPrefix2},
				},
			}

			updateLocalVRFs(localVRFs, clusterVRF, vrf)

			Expect(localVRFs).To(HaveKey("s-tenant-a"))
			sbrVRF := localVRFs["s-tenant-a"]
			Expect(sbrVRF.VRFImports).To(HaveLen(2))
			Expect(clusterVRF.PolicyRoutes).To(HaveLen(2))
			Expect(*clusterVRF.PolicyRoutes[0].TrafficMatch.SrcPrefix).To(Equal(sbrPrefix1))
			Expect(*clusterVRF.PolicyRoutes[1].TrafficMatch.SrcPrefix).To(Equal(sbrPrefix2))
		})

		It("should not create SBR VRF when SBRPrefixes is empty", func() {
			localVRFs := make(map[string]v1alpha1.VRF)
			clusterVRF := createDefaultClusterVRF()
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF:         "tenant-a",
					SBRPrefixes: []string{},
				},
			}

			updateLocalVRFs(localVRFs, clusterVRF, vrf)

			Expect(localVRFs).To(BeEmpty())
			Expect(clusterVRF.PolicyRoutes).To(BeEmpty())
		})
	})

	Describe("appendImportsAsStaticRoutes", func() {
		It("should convert permit imports to static routes", func() {
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF: "tenant-a",
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{CIDR: "10.0.0.0/8", Action: "permit"},
						{CIDR: "192.168.0.0/16", Action: "deny"},
					},
				},
			}

			result := appendImportsAsStaticRoutes(nil, vrf)
			Expect(result).To(HaveLen(1))
			Expect(result[0].Prefix).To(Equal("10.0.0.0/8"))
			Expect(result[0].NextHop).ToNot(BeNil())
			Expect(*result[0].NextHop.Vrf).To(Equal("tenant-a"))
		})
	})

	Describe("dropStaticRouteImports", func() {
		It("should add reject filter items for permit imports", func() {
			clusterVRF := createDefaultClusterVRF()
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF: "tenant-a",
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{CIDR: "10.0.0.0/8", Action: "permit"},
						{CIDR: "192.168.0.0/16", Action: "deny"},
					},
				},
			}

			dropStaticRouteImports(clusterVRF.Redistribute, vrf)
			Expect(clusterVRF.Redistribute.Static.Items).To(HaveLen(1))
			Expect(clusterVRF.Redistribute.Static.Items[0].Matcher.Prefix.Prefix).To(Equal("10.0.0.0/8"))
			Expect(clusterVRF.Redistribute.Static.Items[0].Action.Type).To(Equal(v1alpha1.Reject))
		})
	})

	Describe("buildNodeVrf with ImportModeStaticRoute", func() {
		It("should add static routes and drop redistribute for permit imports", func() {
			crr := newTestCRR(ImportModeStaticRoute)
			node := makeNode("node1", true)

			vni := 1000
			rt := testRouteTarget
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Vrf: []v1alpha1.VRFRevision{
						{
							Name: "vrf-a",
							VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
								VRF:         "tenant-a",
								Seq:         10,
								VNI:         &vni,
								RouteTarget: &rt,
								Import: []v1alpha1.VrfRouteConfigurationPrefixItem{
									{CIDR: "10.0.0.0/8", Action: "permit", Seq: 10},
								},
								Export: []v1alpha1.VrfRouteConfigurationPrefixItem{},
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := crr.buildNodeVrf(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.ClusterVRF.StaticRoutes).To(HaveLen(1))
			Expect(c.Spec.ClusterVRF.StaticRoutes[0].Prefix).To(Equal("10.0.0.0/8"))
			// The static redistribute filter should have a reject item for the imported prefix
			Expect(c.Spec.ClusterVRF.Redistribute.Static.Items).To(HaveLen(1))
		})
	})

	Describe("createFabricVRF", func() {
		It("should use RouteTarget and VNI from spec when provided", func() {
			crr := newTestCRR(ImportModeImport)
			vni := 5000
			rt := "65001:5000"
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF:         "tenant-x",
					VNI:         &vni,
					RouteTarget: &rt,
				},
			}

			fabricVRF, err := crr.createFabricVRF(vrf)
			Expect(err).ToNot(HaveOccurred())
			Expect(fabricVRF.VNI).To(Equal(uint32(5000)))
			Expect(fabricVRF.EVPNImportRouteTargets).To(ContainElement("65001:5000"))
		})

		It("should use config when RouteTarget/VNI not in spec", func() {
			crr := newTestCRR(ImportModeImport)
			vrf := &v1alpha1.VRFRevision{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF: "tenant-a", // present in test config with VNI=1000, RT=65000:1000
				},
			}

			fabricVRF, err := crr.createFabricVRF(vrf)
			Expect(err).ToNot(HaveOccurred())
			Expect(fabricVRF.VNI).To(Equal(uint32(1000)))
			Expect(fabricVRF.EVPNImportRouteTargets).To(ContainElement(testRouteTarget))
		})
	})
})
