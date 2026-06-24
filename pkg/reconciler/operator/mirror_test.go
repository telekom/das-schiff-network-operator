package operator

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func mirrorTestReconciler(objs ...client.Object) *ConfigRevisionReconciler {
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.MirrorTarget{}, &v1alpha1.MirrorSelector{}).
		Build()
	return &ConfigRevisionReconciler{
		logger: ctrl.Log.WithName("test"),
		client: fakeClient,
	}
}

// mirrorRevision returns a revision with a mirror VRF (loopback subnet), an
// external source VRF and a source Layer2.
func mirrorRevision() *v1alpha1.NetworkConfigRevision {
	const subnet = "10.99.0.0/29"
	return &v1alpha1.NetworkConfigRevision{
		Spec: v1alpha1.NetworkConfigRevisionSpec{
			Vrf: []v1alpha1.VRFRevision{
				{
					Name: "mirror-vrf",
					VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
						VRF:       "mirror",
						Loopbacks: []v1alpha1.VRFLoopback{{Name: "lo.mir", Subnet: subnet}},
					},
				},
				{
					Name: "ext",
					VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
						VRF: "external",
					},
				},
			},
			Layer2: []v1alpha1.Layer2Revision{
				{
					Name:                           "vlan100",
					Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{ID: 100},
				},
			},
		},
	}
}

// nodeConfigWithVRFsAndL2 returns a partially-built NodeNetworkConfig with the
// mirror VRF, an external VRF and a source Layer2 already present.
func nodeConfigWithVRFsAndL2() *v1alpha1.NodeNetworkConfig {
	return &v1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "node1"},
		Spec: v1alpha1.NodeNetworkConfigSpec{
			FabricVRFs: map[string]v1alpha1.FabricVRF{
				"mirror": {
					EVPNExportFilter: &v1alpha1.Filter{
						DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
					},
				},
				"external": {
					EVPNExportFilter: &v1alpha1.Filter{
						DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
					},
				},
			},
			Layer2s: map[string]v1alpha1.Layer2{
				"100": {VLAN: 100, VNI: 10100},
			},
		},
	}
}

func mirrorTarget() *v1alpha1.MirrorTarget {
	const name = "collector"
	return &v1alpha1.MirrorTarget{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.MirrorTargetSpec{
			Type:           v1alpha1.MirrorTargetTypeL3GRE,
			DestinationIP:  "10.250.0.100",
			TunnelKey:      ptrUint32(1001),
			DestinationVrf: "mirror",
			SourceLoopback: "lo.mir",
		},
	}
}

func ptrUint32(v uint32) *uint32 { return &v }

func mirrorSelector(name, srcKind, srcName string, dir v1alpha1.MirrorDirection) *v1alpha1.MirrorSelector {
	return &v1alpha1.MirrorSelector{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.MirrorSelectorSpec{
			MirrorTarget: corev1.TypedObjectReference{Kind: "MirrorTarget", Name: "collector"},
			MirrorSource: corev1.TypedObjectReference{Kind: srcKind, Name: srcName},
			Direction:    dir,
		},
	}
}

// mirrorTargetRev and mirrorSelectorRev return the revision-snapshot forms used by
// buildNodeMirror, which now resolves mirror config from the NetworkConfigRevision.
func mirrorTargetRev() v1alpha1.MirrorTargetRevision {
	return v1alpha1.MirrorTargetRevision{Name: "collector", MirrorTargetSpec: mirrorTarget().Spec}
}

func mirrorSelectorRev(name, srcKind, srcName string, dir v1alpha1.MirrorDirection) v1alpha1.MirrorSelectorRevision {
	return v1alpha1.MirrorSelectorRevision{
		Name:               name,
		MirrorSelectorSpec: mirrorSelector(name, srcKind, srcName, dir).Spec,
	}
}

// withMirror attaches mirror target/selector snapshots to a revision.
func withMirror(rev *v1alpha1.NetworkConfigRevision, targets []v1alpha1.MirrorTargetRevision, selectors []v1alpha1.MirrorSelectorRevision) *v1alpha1.NetworkConfigRevision {
	rev.Spec.MirrorTargets = targets
	rev.Spec.MirrorSelectors = selectors
	return rev
}

var _ = Describe("Mirror building", func() {
	Describe("allocateSubnet", func() {
		It("allocates the lowest free host address, skipping network and broadcast", func() {
			result, err := allocateSubnet("10.99.0.0/29", []string{"b", "a"}, nil)
			Expect(err).ToNot(HaveOccurred())
			// Lexical order: "a" gets .1, "b" gets .2.
			Expect(result["a"]).To(Equal("10.99.0.1"))
			Expect(result["b"]).To(Equal("10.99.0.2"))
		})

		It("preserves existing allocations and only allocates for new nodes", func() {
			existing := map[string]string{"a": "10.99.0.5"}
			result, err := allocateSubnet("10.99.0.0/29", []string{"a", "b"}, existing)
			Expect(err).ToNot(HaveOccurred())
			Expect(result["a"]).To(Equal("10.99.0.5"))
			Expect(result["b"]).To(Equal("10.99.0.1"))
		})

		It("does not re-emit out-of-scope nodes but reserves their addresses", func() {
			// "gone" is out of scope (e.g. NotReady) but its NodeNetworkConfig still
			// carries 10.99.0.1, so that address must not be handed to another node.
			existing := map[string]string{"gone": "10.99.0.1"}
			result, err := allocateSubnet("10.99.0.0/29", []string{"a"}, existing)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(HaveKey("gone"))
			Expect(result["a"]).To(Equal("10.99.0.2"), "must skip the address reserved by the still-configured out-of-scope node")
		})

		It("reallocates a stored IP that is no longer inside the (changed) subnet", func() {
			// "a" was previously allocated from a different subnet; after the subnet
			// changed it must not keep the out-of-subnet address.
			existing := map[string]string{"a": "10.50.0.1"}
			result, err := allocateSubnet("10.99.0.0/29", []string{"a"}, existing)
			Expect(err).ToNot(HaveOccurred())
			Expect(result["a"]).To(Equal("10.99.0.1"))
		})

		It("returns an error for an invalid CIDR", func() {
			_, err := allocateSubnet("not-a-cidr", []string{"a"}, nil)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("greInterfaceName", func() {
		It("is deterministic, prefixed by type and within the Linux name limit", func() {
			l3 := greInterfaceName("collector", v1alpha1.MirrorTargetTypeL3GRE)
			l2 := greInterfaceName("collector", v1alpha1.MirrorTargetTypeL2GRE)
			Expect(l3).To(Equal(greInterfaceName("collector", v1alpha1.MirrorTargetTypeL3GRE)))
			Expect(l3).To(HavePrefix("gre-"))
			Expect(l2).To(HavePrefix("gtap-"))
			Expect(len(l3)).To(BeNumerically("<=", 15))
			Expect(len(l2)).To(BeNumerically("<=", 15))
		})
	})

	Describe("hostAddress", func() {
		It("uses /32 for IPv4 and /128 for IPv6", func() {
			Expect(hostAddress("10.0.0.1")).To(Equal("10.0.0.1/32"))
			Expect(hostAddress("fd00::1")).To(Equal("fd00::1/128"))
		})
	})

	Describe("buildNodeMirror", func() {
		It("injects GRE, loopback, export-filter entry and MirrorACLs into the node config", func() {
			node := makeNode("node1", true)

			crr := mirrorTestReconciler(node)
			revision := withMirror(mirrorRevision(),
				[]v1alpha1.MirrorTargetRevision{mirrorTargetRev()},
				[]v1alpha1.MirrorSelectorRevision{
					mirrorSelectorRev("sel-l2", "Layer2NetworkConfiguration", "vlan100", v1alpha1.MirrorDirectionIngress),
					mirrorSelectorRev("sel-vrf", "VRFRouteConfiguration", "ext", v1alpha1.MirrorDirectionEgress),
				})
			c := nodeConfigWithVRFsAndL2()

			Expect(crr.buildNodeMirror(context.Background(), node, revision, c)).To(Succeed())

			mirrorVrf := c.Spec.FabricVRFs["mirror"]
			Expect(mirrorVrf.Loopbacks).To(HaveKey("lo.mir"))
			Expect(mirrorVrf.Loopbacks["lo.mir"].IPAddresses).To(ConsistOf("10.99.0.1/32"))

			greName := greInterfaceName("collector", v1alpha1.MirrorTargetTypeL3GRE)
			Expect(mirrorVrf.GREs).To(HaveKey(greName))
			gre := mirrorVrf.GREs[greName]
			Expect(gre.SourceAddress).To(Equal("10.99.0.1"))
			Expect(gre.SourceInterface).To(Equal("lo.mir"))
			Expect(gre.DestinationAddress).To(Equal("10.250.0.100"))
			Expect(gre.Layer).To(Equal(v1alpha1.GRELayer3))
			Expect(gre.EncapsulationKey).ToNot(BeNil())
			Expect(*gre.EncapsulationKey).To(Equal(uint32(1001)))

			var hasPermit bool
			for _, item := range mirrorVrf.EVPNExportFilter.Items {
				if item.Matcher.Prefix != nil && item.Matcher.Prefix.Prefix == "10.99.0.1/32" {
					hasPermit = item.Action.Type == v1alpha1.Accept
				}
			}
			Expect(hasPermit).To(BeTrue(), "mirror source IP should be permitted in the EVPN export filter")

			Expect(c.Spec.Layer2s["100"].MirrorACLs).To(HaveLen(1))
			Expect(c.Spec.Layer2s["100"].MirrorACLs[0].MirrorDestination).To(Equal(greName))
			Expect(c.Spec.Layer2s["100"].MirrorACLs[0].Direction).To(Equal(v1alpha1.MirrorDirectionIngress))

			Expect(c.Spec.FabricVRFs["external"].MirrorACLs).To(HaveLen(1))
			Expect(c.Spec.FabricVRFs["external"].MirrorACLs[0].Direction).To(Equal(v1alpha1.MirrorDirectionEgress))
		})

		It("shares a single GRE tunnel between selectors that reference the same target", func() {
			node := makeNode("node1", true)

			crr := mirrorTestReconciler(node)
			revision := withMirror(mirrorRevision(),
				[]v1alpha1.MirrorTargetRevision{mirrorTargetRev()},
				[]v1alpha1.MirrorSelectorRevision{
					mirrorSelectorRev("sel-l2", "Layer2NetworkConfiguration", "vlan100", v1alpha1.MirrorDirectionIngress),
					mirrorSelectorRev("sel-vrf", "VRFRouteConfiguration", "ext", v1alpha1.MirrorDirectionEgress),
				})
			c := nodeConfigWithVRFsAndL2()

			Expect(crr.buildNodeMirror(context.Background(), node, revision, c)).To(Succeed())
			Expect(c.Spec.FabricVRFs["mirror"].GREs).To(HaveLen(1))
		})

		It("does nothing when no selectors exist", func() {
			node := makeNode("node1", true)
			crr := mirrorTestReconciler(node)
			c := nodeConfigWithVRFsAndL2()
			Expect(crr.buildNodeMirror(context.Background(), node, mirrorRevision(), c)).To(Succeed())
			Expect(c.Spec.FabricVRFs["mirror"].GREs).To(BeEmpty())
		})

		It("skips the selector when the mirror VRF is absent on the node", func() {
			node := makeNode("node1", true)

			crr := mirrorTestReconciler(node)
			revision := withMirror(mirrorRevision(),
				[]v1alpha1.MirrorTargetRevision{mirrorTargetRev()},
				[]v1alpha1.MirrorSelectorRevision{
					mirrorSelectorRev("sel-l2", "Layer2NetworkConfiguration", "vlan100", v1alpha1.MirrorDirectionIngress),
				})
			c := nodeConfigWithVRFsAndL2()
			delete(c.Spec.FabricVRFs, "mirror")

			Expect(crr.buildNodeMirror(context.Background(), node, revision, c)).To(Succeed())
			Expect(c.Spec.Layer2s["100"].MirrorACLs).To(BeEmpty())
		})

		It("does not inject a tunnel/loopback when the selector's source is absent", func() {
			node := makeNode("node1", true)

			crr := mirrorTestReconciler(node)
			revision := withMirror(mirrorRevision(),
				[]v1alpha1.MirrorTargetRevision{mirrorTargetRev()},
				[]v1alpha1.MirrorSelectorRevision{
					mirrorSelectorRev("sel-l2", "Layer2NetworkConfiguration", "vlan100", v1alpha1.MirrorDirectionIngress),
				})
			c := nodeConfigWithVRFsAndL2()
			// The mirror VRF is present, but the selector's source Layer2 is not.
			delete(c.Spec.Layer2s, "100")

			Expect(crr.buildNodeMirror(context.Background(), node, revision, c)).To(Succeed())
			// No GRE tunnel or loopback should be injected for a source-less selector.
			Expect(c.Spec.FabricVRFs["mirror"].GREs).To(BeEmpty())
			Expect(c.Spec.FabricVRFs["mirror"].Loopbacks).To(BeEmpty())
		})

		It("allocates IPv6 source addresses and a /128 loopback for an IPv6 collector", func() {
			node := makeNode("node1", true)
			ipv6Target := v1alpha1.MirrorTargetRevision{
				Name: "collector",
				MirrorTargetSpec: v1alpha1.MirrorTargetSpec{
					Type:           v1alpha1.MirrorTargetTypeL3GRE,
					DestinationIP:  "fd00:caa5:9000::100",
					DestinationVrf: "mirror",
					SourceLoopback: "lo.mir",
				},
			}
			revision := withMirror(mirrorRevision(),
				[]v1alpha1.MirrorTargetRevision{ipv6Target},
				[]v1alpha1.MirrorSelectorRevision{
					mirrorSelectorRev("sel-l2", "Layer2NetworkConfiguration", "vlan100", v1alpha1.MirrorDirectionIngress),
				})
			revision.Spec.Vrf[0].Loopbacks = []v1alpha1.VRFLoopback{{Name: "lo.mir", Subnet: "fd00:caa5:5599::/125"}}

			crr := mirrorTestReconciler(node)
			c := nodeConfigWithVRFsAndL2()

			Expect(crr.buildNodeMirror(context.Background(), node, revision, c)).To(Succeed())

			mirrorVrf := c.Spec.FabricVRFs["mirror"]
			Expect(mirrorVrf.Loopbacks["lo.mir"].IPAddresses).To(ConsistOf("fd00:caa5:5599::1/128"))

			greName := greInterfaceName("collector", v1alpha1.MirrorTargetTypeL3GRE)
			Expect(mirrorVrf.GREs[greName].SourceAddress).To(Equal("fd00:caa5:5599::1"))
			Expect(mirrorVrf.GREs[greName].DestinationAddress).To(Equal("fd00:caa5:9000::100"))

			var hasPermit bool
			for _, item := range mirrorVrf.EVPNExportFilter.Items {
				if item.Matcher.Prefix != nil && item.Matcher.Prefix.Prefix == "fd00:caa5:5599::1/128" {
					hasPermit = item.Action.Type == v1alpha1.Accept
				}
			}
			Expect(hasPermit).To(BeTrue(), "IPv6 mirror source /128 should be permitted in the EVPN export filter")
		})
	})

	Describe("reconcileMirrorStatus", func() {
		It("reports ActiveSelectors/ActiveNodes and conditions from the deployed configs", func() {
			target := mirrorTarget()
			sel := mirrorSelector("sel-l2", "Layer2NetworkConfiguration", "vlan100", v1alpha1.MirrorDirectionIngress)
			source := &v1alpha1.Layer2NetworkConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "vlan100"},
				Spec:       v1alpha1.Layer2NetworkConfigurationSpec{ID: 100},
			}

			greName := greInterfaceName("collector", v1alpha1.MirrorTargetTypeL3GRE)
			nnc := &v1alpha1.NodeNetworkConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "node1"},
				Spec: v1alpha1.NodeNetworkConfigSpec{
					FabricVRFs: map[string]v1alpha1.FabricVRF{
						"mirror": {VRF: v1alpha1.VRF{GREs: map[string]v1alpha1.GRE{greName: {SourceAddress: "10.99.0.1"}}}},
					},
					Layer2s: map[string]v1alpha1.Layer2{
						"100": {VLAN: 100, MirrorACLs: []v1alpha1.MirrorACL{
							{MirrorDestination: greName, Direction: v1alpha1.MirrorDirectionIngress},
						}},
					},
				},
			}

			crr := mirrorTestReconciler(target, sel, source, nnc)
			Expect(crr.reconcileMirrorStatus(context.Background())).To(Succeed())

			gotTarget := &v1alpha1.MirrorTarget{}
			Expect(crr.client.Get(context.Background(), types.NamespacedName{Name: "collector"}, gotTarget)).To(Succeed())
			Expect(gotTarget.Status.ActiveSelectors).To(Equal(1))
			Expect(gotTarget.Status.ActiveNodes).To(Equal(1))
			Expect(meta.IsStatusConditionTrue(gotTarget.Status.Conditions, conditionReady)).To(BeTrue())

			gotSel := &v1alpha1.MirrorSelector{}
			Expect(crr.client.Get(context.Background(), types.NamespacedName{Name: "sel-l2"}, gotSel)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(gotSel.Status.Conditions, conditionResolved)).To(BeTrue())
			Expect(meta.IsStatusConditionTrue(gotSel.Status.Conditions, conditionApplied)).To(BeTrue())
		})

		It("does not mark a selector Applied when only the tunnel exists but its source has no ACL", func() {
			target := mirrorTarget()
			// Another selector created the tunnel; this selector's source carries no ACL.
			sel := mirrorSelector("sel-l2", "Layer2NetworkConfiguration", "vlan100", v1alpha1.MirrorDirectionIngress)
			source := &v1alpha1.Layer2NetworkConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "vlan100"},
				Spec:       v1alpha1.Layer2NetworkConfigurationSpec{ID: 100},
			}

			greName := greInterfaceName("collector", v1alpha1.MirrorTargetTypeL3GRE)
			nnc := &v1alpha1.NodeNetworkConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "node1"},
				Spec: v1alpha1.NodeNetworkConfigSpec{
					FabricVRFs: map[string]v1alpha1.FabricVRF{
						"mirror": {VRF: v1alpha1.VRF{GREs: map[string]v1alpha1.GRE{greName: {SourceAddress: "10.99.0.1"}}}},
					},
					Layer2s: map[string]v1alpha1.Layer2{
						"100": {VLAN: 100},
					},
				},
			}

			crr := mirrorTestReconciler(target, sel, source, nnc)
			Expect(crr.reconcileMirrorStatus(context.Background())).To(Succeed())

			gotSel := &v1alpha1.MirrorSelector{}
			Expect(crr.client.Get(context.Background(), types.NamespacedName{Name: "sel-l2"}, gotSel)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(gotSel.Status.Conditions, conditionResolved)).To(BeTrue())
			Expect(meta.IsStatusConditionFalse(gotSel.Status.Conditions, conditionApplied)).To(BeTrue())
		})

		It("marks a selector unresolved when its source is missing", func() {
			target := mirrorTarget()
			sel := mirrorSelector("sel-l2", "Layer2NetworkConfiguration", "vlan100", v1alpha1.MirrorDirectionIngress)
			crr := mirrorTestReconciler(target, sel)
			Expect(crr.reconcileMirrorStatus(context.Background())).To(Succeed())

			gotSel := &v1alpha1.MirrorSelector{}
			Expect(crr.client.Get(context.Background(), types.NamespacedName{Name: "sel-l2"}, gotSel)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(gotSel.Status.Conditions, conditionResolved)).To(BeTrue())
		})

		It("marks a selector unresolved when its target is missing", func() {
			sel := mirrorSelector("orphan", "Layer2NetworkConfiguration", "vlan100", v1alpha1.MirrorDirectionIngress)
			crr := mirrorTestReconciler(sel)
			Expect(crr.reconcileMirrorStatus(context.Background())).To(Succeed())

			gotSel := &v1alpha1.MirrorSelector{}
			Expect(crr.client.Get(context.Background(), types.NamespacedName{Name: "orphan"}, gotSel)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(gotSel.Status.Conditions, conditionResolved)).To(BeTrue())
		})
	})
})
