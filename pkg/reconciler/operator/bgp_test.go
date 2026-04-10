package operator

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("BGP building", func() {
	Describe("convertIPToCIDR", func() {
		DescribeTable("converts IP addresses to CIDR notation",
			func(ip, expected string, expectErr bool) {
				result, err := convertIPToCIDR(ip)
				if expectErr {
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("failed to parse IP address"))
				} else {
					Expect(err).ToNot(HaveOccurred())
					Expect(result).To(Equal(expected))
				}
			},
			Entry("IPv4 address", "10.0.0.1", "10.0.0.1/32", false),
			Entry("IPv6 address", "2001:db8::1", "2001:db8::1/128", false),
			Entry("invalid IP", "not-an-ip", "", true),
			Entry("empty string", "", "", true),
		)
	})

	Describe("buildBgpAddressFamily", func() {
		It("should build IPv4 address family with export and import filter items", func() {
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					Export: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit"},
					},
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{CIDR: "192.168.0.0/16", Seq: 10, Action: "permit"},
					},
				},
			}

			af, err := buildBgpAddressFamily(peer, IPv4)
			Expect(err).ToNot(HaveOccurred())
			Expect(af).ToNot(BeNil())
			Expect(af.ExportFilter.Items).To(HaveLen(1))
			Expect(af.ImportFilter.Items).To(HaveLen(1))
			Expect(af.ExportFilter.Items[0].Action.Type).To(Equal(v1alpha1.Accept))
		})

		It("should build IPv6 address family with correct filtering", func() {
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					Export: []v1alpha1.VrfRouteConfigurationPrefixItem{
						{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit"},    // IPv4 - filtered out
						{CIDR: "2001:db8::/32", Seq: 20, Action: "permit"}, // IPv6 - included
					},
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}

			af, err := buildBgpAddressFamily(peer, IPv6)
			Expect(err).ToNot(HaveOccurred())
			Expect(af).ToNot(BeNil())
			Expect(af.ExportFilter.Items).To(HaveLen(1))
			Expect(af.ExportFilter.Items[0].Matcher.Prefix.Prefix).To(Equal("2001:db8::/32"))
		})

		It("should set MaxPrefixes when provided", func() {
			maxPrefixes := uint32(100)
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					MaximumPrefixes: &maxPrefixes,
					Export:          []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:          []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}

			af, err := buildBgpAddressFamily(peer, IPv4)
			Expect(err).ToNot(HaveOccurred())
			Expect(af.MaxPrefixes).To(Equal(&maxPrefixes))
		})
	})

	Describe("buildBgpPeer", func() {
		It("should build peer with IPv4 loopback address and multihop", func() {
			loopbackIP := "10.0.0.1"
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					RemoteASN: 65000,
					Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}

			bgpPeer, err := buildBgpPeer(&loopbackIP, nil, peer)
			Expect(err).ToNot(HaveOccurred())
			Expect(bgpPeer).ToNot(BeNil())
			Expect(bgpPeer.Address).To(Equal(&loopbackIP))
			Expect(bgpPeer.Multihop).ToNot(BeNil())
			Expect(*bgpPeer.Multihop).To(Equal(bgpMultihop))
			Expect(bgpPeer.RemoteASN).To(Equal(uint32(65000)))
			Expect(bgpPeer.IPv4).ToNot(BeNil())
			Expect(bgpPeer.IPv6).To(BeNil())
		})

		It("should build peer with IPv6 loopback address", func() {
			loopbackIP := "2001:db8::1"
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					RemoteASN: 65000,
					Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}

			bgpPeer, err := buildBgpPeer(&loopbackIP, nil, peer)
			Expect(err).ToNot(HaveOccurred())
			Expect(bgpPeer).ToNot(BeNil())
			Expect(bgpPeer.Address).To(Equal(&loopbackIP))
			Expect(bgpPeer.Multihop).ToNot(BeNil())
			// IPv6 loopback gets both IPv4 (loopbackIP != nil) and IPv6 (family == IPv6) address families
			Expect(bgpPeer.IPv4).ToNot(BeNil())
			Expect(bgpPeer.IPv6).ToNot(BeNil())
		})

		It("should build peer with listen range", func() {
			listenRange := "10.0.0.0/24"
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					RemoteASN: 65000,
					Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}

			bgpPeer, err := buildBgpPeer(nil, &listenRange, peer)
			Expect(err).ToNot(HaveOccurred())
			Expect(bgpPeer).ToNot(BeNil())
			Expect(bgpPeer.ListenRange).To(Equal(&listenRange))
			Expect(bgpPeer.Multihop).To(BeNil()) // no multihop for listen range
			Expect(bgpPeer.IPv4).ToNot(BeNil())
			Expect(bgpPeer.IPv6).To(BeNil())
		})

		It("should use IPv6 family for IPv6 listen range", func() {
			listenRange := "2001:db8::/32"
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					RemoteASN: 65000,
					Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}

			bgpPeer, err := buildBgpPeer(nil, &listenRange, peer)
			Expect(err).ToNot(HaveOccurred())
			Expect(bgpPeer.IPv6).ToNot(BeNil())
			Expect(bgpPeer.IPv4).To(BeNil())
		})

		It("should return error for invalid loopback IP", func() {
			loopbackIP := "not-an-ip"
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					RemoteASN: 65000,
					Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}

			_, err := buildBgpPeer(&loopbackIP, nil, peer)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to parse loopback IP"))
		})

		It("should return error for invalid listen range CIDR", func() {
			listenRange := "not-a-cidr"
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					RemoteASN: 65000,
					Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}

			_, err := buildBgpPeer(nil, &listenRange, peer)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to parse listen range"))
		})
	})

	Describe("buildPeeringVlanPeer", func() {
		It("should return error when PeeringVlan is nil", func() {
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					PeeringVlan: nil,
					RemoteASN:   65000,
					Export:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			node := makeNode("node1", true)
			revision := &v1alpha1.NetworkConfigRevision{}
			c := &v1alpha1.NodeNetworkConfig{
				Spec: v1alpha1.NodeNetworkConfigSpec{
					ClusterVRF: &v1alpha1.VRF{},
					FabricVRFs: make(map[string]v1alpha1.FabricVRF),
				},
			}

			err := buildPeeringVlanPeer(node, peer, revision, c)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("peering VLAN is nil"))
		})

		It("should return error when no matching layer2 has IRB IPs", func() {
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					PeeringVlan: &v1alpha1.BGPPeeringVLAN{Name: "l2-100"},
					RemoteASN:   65000,
					Export:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			node := makeNode("node1", true)
			// Revision with matching l2 but no anycast gateways
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-100",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:              100,
								VNI:             1000,
								MTU:             1500,
								AnycastGateways: []string{}, // no IPs
							},
						},
					},
				},
			}
			c := &v1alpha1.NodeNetworkConfig{
				Spec: v1alpha1.NodeNetworkConfigSpec{
					ClusterVRF: &v1alpha1.VRF{},
					FabricVRFs: make(map[string]v1alpha1.FabricVRF),
				},
			}

			err := buildPeeringVlanPeer(node, peer, revision, c)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no IRB IPs found for peering VLAN"))
		})

		It("should add peer to cluster VRF when VRF is empty", func() {
			// Design quirk: when the Layer2 network's VRF field is an empty string, the BGP
			// peer is intentionally placed in the ClusterVRF rather than a FabricVRF.
			// This is the expected routing for L2 networks that are not tenant-bound —
			// they belong to the cluster's own VRF (i.e. the underlay). An empty VRF on
			// a Layer2 revision means "no tenant isolation", so peering is cluster-level.
			// Future maintainers: do NOT change this to an error — empty VRF is valid.
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					PeeringVlan: &v1alpha1.BGPPeeringVLAN{Name: "l2-100"},
					RemoteASN:   65000,
					Export:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			node := makeNode("node1", true)
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-100",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:              100,
								VNI:             1000,
								MTU:             1500,
								AnycastGateways: []string{"10.0.0.1/24"},
								VRF:             "", // empty VRF = cluster-level (no tenant isolation)
							},
						},
					},
				},
			}
			c := &v1alpha1.NodeNetworkConfig{
				Spec: v1alpha1.NodeNetworkConfigSpec{
					ClusterVRF: &v1alpha1.VRF{},
					FabricVRFs: make(map[string]v1alpha1.FabricVRF),
				},
			}

			err := buildPeeringVlanPeer(node, peer, revision, c)
			Expect(err).ToNot(HaveOccurred())
			// Peer must land in ClusterVRF, not FabricVRFs, because Layer2.VRF is empty.
			Expect(c.Spec.ClusterVRF.BGPPeers).To(HaveLen(1))
		})

		It("should add peer to fabric VRF when VRF is set", func() {
			peer := &v1alpha1.BGPRevision{
				BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
					PeeringVlan: &v1alpha1.BGPPeeringVLAN{Name: "l2-100"},
					RemoteASN:   65000,
					Export:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Import:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			node := makeNode("node1", true)
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-100",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:              100,
								VNI:             1000,
								MTU:             1500,
								AnycastGateways: []string{"10.0.0.1/24"},
								VRF:             "tenant-a",
							},
						},
					},
				},
			}
			c := &v1alpha1.NodeNetworkConfig{
				Spec: v1alpha1.NodeNetworkConfigSpec{
					ClusterVRF: &v1alpha1.VRF{},
					FabricVRFs: map[string]v1alpha1.FabricVRF{
						"tenant-a": {
							EVPNExportFilter: &v1alpha1.Filter{
								DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
							},
						},
					},
				},
			}

			err := buildPeeringVlanPeer(node, peer, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.FabricVRFs["tenant-a"].BGPPeers).To(HaveLen(1))
		})
	})

	Describe("buildNodeBgpPeers", func() {
		It("should skip peers that don't match node selector", func() {
			node := makeNode("node1", true)
			node.Labels = map[string]string{"role": "worker"}

			loopbackIP := "10.0.0.1"
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					BGP: []v1alpha1.BGPRevision{
						{
							Name: "bgp-master",
							BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
								LoopbackPeer: &v1alpha1.BGPPeeringLoopback{
									IPAddresses: []string{loopbackIP},
								},
								RemoteASN: 65000,
								Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
								NodeSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"role": "master"},
								},
							},
						},
					},
				},
			}
			c := &v1alpha1.NodeNetworkConfig{
				Spec: v1alpha1.NodeNetworkConfigSpec{
					ClusterVRF: &v1alpha1.VRF{},
					FabricVRFs: make(map[string]v1alpha1.FabricVRF),
				},
			}

			err := buildNodeBgpPeers(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.ClusterVRF.BGPPeers).To(BeEmpty())
		})

		// NOTE: This spec mutates package-level state (hbnHostNextHop) and the process
		// environment. It must not run in parallel with other specs in this package.
		It("should add loopback BGP peers with static routes", func() {
			prevHbnHostNextHop := hbnHostNextHop
			prevEnvValue, prevEnvWasSet := os.LookupEnv("HBN_HOST_NEXTHOP")
			DeferCleanup(func() {
				hbnHostNextHop = prevHbnHostNextHop
				if prevEnvWasSet {
					Expect(os.Setenv("HBN_HOST_NEXTHOP", prevEnvValue)).To(Succeed())
				} else {
					Expect(os.Unsetenv("HBN_HOST_NEXTHOP")).To(Succeed())
				}
			})
			hbnHostNextHop = "192.0.2.1"

			loopbackIP := "10.0.0.2"
			node := makeNode("node1", true)
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					BGP: []v1alpha1.BGPRevision{
						{
							Name: "bgp-peer",
							BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
								LoopbackPeer: &v1alpha1.BGPPeeringLoopback{
									IPAddresses: []string{loopbackIP},
								},
								RemoteASN: 65000,
								Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
							},
						},
					},
				},
			}
			c := &v1alpha1.NodeNetworkConfig{
				Spec: v1alpha1.NodeNetworkConfigSpec{
					ClusterVRF: &v1alpha1.VRF{},
					FabricVRFs: make(map[string]v1alpha1.FabricVRF),
				},
			}

			err := buildNodeBgpPeers(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.ClusterVRF.BGPPeers).To(HaveLen(1))
			Expect(c.Spec.ClusterVRF.StaticRoutes).To(HaveLen(1))
			Expect(c.Spec.ClusterVRF.StaticRoutes[0].Prefix).To(Equal("10.0.0.2/32"))
		})

		It("should return error when BGP peer has no loopback IPs and no peering VLAN", func() {
			node := makeNode("node1", true)
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					BGP: []v1alpha1.BGPRevision{
						{
							Name: "bgp-invalid",
							BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
								RemoteASN: 65000,
								Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
							},
						},
					},
				},
			}
			c := &v1alpha1.NodeNetworkConfig{
				Spec: v1alpha1.NodeNetworkConfigSpec{
					ClusterVRF: &v1alpha1.VRF{},
					FabricVRFs: make(map[string]v1alpha1.FabricVRF),
				},
			}

			err := buildNodeBgpPeers(node, revision, c)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no loopback IPs found for BGP peer"))
		})
	})

	Describe("buildNetplanDummies", func() {
		It("should create dummy interfaces for loopback BGP peers", func() {
			node := makeNode("node1", true)
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					BGP: []v1alpha1.BGPRevision{
						{
							Name: "bgp-peer",
							BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
								LoopbackPeer: &v1alpha1.BGPPeeringLoopback{
									IPAddresses: []string{"10.0.0.2"},
								},
								RemoteASN: 65000,
								Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
							},
						},
					},
				},
			}

			dummies, err := buildNetplanDummies(node, revision)
			Expect(err).ToNot(HaveOccurred())
			Expect(dummies).To(HaveLen(1))
		})

		It("should skip peers that don't match node selector", func() {
			node := makeNode("node1", true)
			node.Labels = map[string]string{"role": "worker"}

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					BGP: []v1alpha1.BGPRevision{
						{
							Name: "bgp-peer",
							BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
								LoopbackPeer: &v1alpha1.BGPPeeringLoopback{
									IPAddresses: []string{"10.0.0.2"},
								},
								RemoteASN: 65000,
								Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
								NodeSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"role": "master"},
								},
							},
						},
					},
				},
			}

			dummies, err := buildNetplanDummies(node, revision)
			Expect(err).ToNot(HaveOccurred())
			Expect(dummies).To(BeEmpty())
		})

		It("should skip BGP entries without loopback peers", func() {
			node := makeNode("node1", true)
			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					BGP: []v1alpha1.BGPRevision{
						{
							Name: "bgp-vlan",
							BGPPeeringSpec: v1alpha1.BGPPeeringSpec{
								PeeringVlan: &v1alpha1.BGPPeeringVLAN{Name: "l2-100"},
								RemoteASN:   65000,
								Export:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
								Import:      []v1alpha1.VrfRouteConfigurationPrefixItem{},
							},
						},
					},
				},
			}

			dummies, err := buildNetplanDummies(node, revision)
			Expect(err).ToNot(HaveOccurred())
			Expect(dummies).To(BeEmpty())
		})
	})
})
