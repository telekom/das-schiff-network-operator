package operator

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
)

var _ = Describe("ConfigRevisionReconciler helpers", func() {
	const previousConfigHash = "old-config"
	var logger logr.Logger

	BeforeEach(func() {
		logger = ctrl.Log.WithName("test")
	})

	Describe("getFirstValidRevision", func() {
		It("should return the first non-invalid revision", func() {
			revisions := []v1alpha1.NetworkConfigRevision{
				makeRevision("invalid1", true, time.Now()),
				makeRevision("valid001", false, time.Now().Add(-time.Minute)),
				makeRevision("valid002", false, time.Now().Add(-2*time.Minute)),
			}
			result := getFirstValidRevision(revisions)
			Expect(result).ToNot(BeNil())
			Expect(result.Spec.Revision).To(Equal("valid001"))
		})

		It("should return nil when all revisions are invalid", func() {
			revisions := []v1alpha1.NetworkConfigRevision{
				makeRevision("invalid1", true, time.Now()),
				makeRevision("invalid2", true, time.Now().Add(-time.Minute)),
			}
			result := getFirstValidRevision(revisions)
			Expect(result).To(BeNil())
		})

		It("should return nil for empty list", func() {
			result := getFirstValidRevision([]v1alpha1.NetworkConfigRevision{})
			Expect(result).To(BeNil())
		})
	})

	Describe("getRevisionCounters", func() {
		var crr *ConfigRevisionReconciler

		BeforeEach(func() {
			crr = &ConfigRevisionReconciler{
				logger:           logger,
				configTimeout:    5 * time.Minute,
				preconfigTimeout: 10 * time.Minute,
			}
		})

		It("should count provisioned nodes correctly", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", StatusProvisioned, time.Now().Add(-time.Minute)),
				makeNodeConfig("node2", "rev001", StatusProvisioned, time.Now().Add(-time.Minute)),
			}
			cnt := crr.getRevisionCounters(configs, &revision)
			Expect(cnt.ready).To(Equal(2))
			Expect(cnt.ongoing).To(Equal(0))
			Expect(cnt.invalid).To(Equal(0))
		})

		It("should count provisioning nodes correctly", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", StatusProvisioning, time.Now()),
			}
			cnt := crr.getRevisionCounters(configs, &revision)
			Expect(cnt.ready).To(Equal(0))
			Expect(cnt.ongoing).To(Equal(1))
			Expect(cnt.invalid).To(Equal(0))
		})

		It("should count invalid nodes correctly", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", StatusInvalid, time.Now().Add(-time.Minute)),
			}
			cnt := crr.getRevisionCounters(configs, &revision)
			Expect(cnt.ready).To(Equal(0))
			Expect(cnt.ongoing).To(Equal(0))
			Expect(cnt.invalid).To(Equal(1))
		})

		It("should count nodes with empty status as ongoing (pre-config)", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", "", time.Now()),
			}
			cnt := crr.getRevisionCounters(configs, &revision)
			Expect(cnt.ready).To(Equal(0))
			Expect(cnt.ongoing).To(Equal(1))
			Expect(cnt.invalid).To(Equal(0))
		})

		It("should count timed-out provisioning config as invalid (still ongoing)", func() {
			crr.configTimeout = 50 * time.Millisecond
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", StatusProvisioning, time.Now().Add(-time.Minute)),
			}
			cnt := crr.getRevisionCounters(configs, &revision)
			Expect(cnt.ongoing).To(Equal(1)) // still counted as ongoing
			Expect(cnt.invalid).To(Equal(1)) // also counted as invalid because timeout reached
			Expect(cnt.ready).To(Equal(0))
		})

		It("should not count configs for other revisions", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev002", StatusProvisioned, time.Now().Add(-time.Minute)),
			}
			cnt := crr.getRevisionCounters(configs, &revision)
			Expect(cnt.ready).To(Equal(0))
			Expect(cnt.ongoing).To(Equal(0))
			Expect(cnt.invalid).To(Equal(0))
		})

		DescribeTable("ignoring terminal status from a previous resolved configuration",
			func(status string) {
				revision := makeRevision("rev001", false, time.Now())
				cfg := makeNodeConfig("node1", "rev001", status, time.Now().Add(-time.Hour))
				cfg.Spec.ConfigHash = "new-config"
				cfg.Status.LastAppliedConfigHash = previousConfigHash
				now := metav1.Now()
				cfg.Spec.ConfigUpdateTime = &now
				cnt := crr.getRevisionCounters([]v1alpha1.NodeNetworkConfig{cfg}, &revision)
				Expect(cnt.ready).To(BeZero())
				Expect(cnt.invalid).To(BeZero())
				Expect(cnt.ongoing).To(Equal(1))
			},
			Entry("provisioned", StatusProvisioned),
			Entry("invalid", StatusInvalid),
		)

		DescribeTable("bounding unacknowledged configuration updates",
			func(local bool) {
				revision := makeRevision("rev001", false, time.Now())
				start := metav1.NewTime(time.Now().Add(-time.Hour))
				cfg := makeNodeConfig("node1", "rev001", StatusProvisioned, start.Time.Add(-time.Hour))
				cfg.Spec.ConfigHash, cfg.Status.LastAppliedConfigHash = "new-config", previousConfigHash
				cfg.Spec.ConfigUpdateTime = &start
				if local {
					cfg.Annotations = map[string]string{nodeLocalRevisionAnnotation: revision.Spec.Revision}
				}
				cnt := crr.getRevisionCounters([]v1alpha1.NodeNetworkConfig{cfg}, &revision)
				Expect(cnt.ready).To(BeZero())
				Expect(cnt.invalid).To(Equal(1))
				Expect(cnt.revisionInvalid).To(Equal(!local))
				if local {
					Expect(cnt.ongoing).To(BeZero())
				} else {
					Expect(cnt.ongoing).To(Equal(1))
				}
			},
			Entry("node-local update releases its rollout slot", true),
			Entry("global update retains revision invalidation", false),
		)
	})

	Describe("wasConfigTimeoutReached", func() {
		It("should return false when LastUpdate is zero time", func() {
			cfg := &v1alpha1.NodeNetworkConfig{}
			cfg.Status.LastUpdate = metav1.Time{}
			Expect(wasConfigTimeoutReached(cfg, time.Minute)).To(BeFalse())
		})

		It("should return false when within timeout", func() {
			cfg := &v1alpha1.NodeNetworkConfig{}
			cfg.Status.LastUpdate = metav1.NewTime(time.Now())
			Expect(wasConfigTimeoutReached(cfg, 10*time.Minute)).To(BeFalse())
		})

		It("should return true when timeout is exceeded", func() {
			cfg := &v1alpha1.NodeNetworkConfig{}
			cfg.Status.LastUpdate = metav1.NewTime(time.Now().Add(-10 * time.Minute))
			Expect(wasConfigTimeoutReached(cfg, time.Minute)).To(BeTrue())
		})
	})

	Describe("nodeConfigHash", func() {
		It("should ignore provenance and hash maps deterministically", func() {
			spec := v1alpha1.NodeNetworkConfigSpec{
				Revision: "first", ConfigHash: "previous",
				Layer2s: map[string]v1alpha1.Layer2{"100": {VLAN: 100}, "200": {VLAN: 200}},
			}
			netplanSpec := v1alpha1.NodeNetplanConfigSpec{}
			hash, err := nodeConfigHash(spec, &netplanSpec)
			Expect(err).ToNot(HaveOccurred())
			spec.Revision, spec.ConfigHash = "second", ""
			now := metav1.Now()
			spec.ConfigUpdateTime = &now
			spec.Layer2s = map[string]v1alpha1.Layer2{"200": {VLAN: 200}, "100": {VLAN: 100}}
			otherHash, err := nodeConfigHash(spec, &netplanSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(otherHash).To(Equal(hash))
			spec.Layer2s["100"] = v1alpha1.Layer2{VLAN: 100, MTU: 9000}
			otherHash, err = nodeConfigHash(spec, &netplanSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(otherHash).ToNot(Equal(hash))
		})

		It("should include Netplan-only settings and surface invalid device JSON", func() {
			spec := v1alpha1.NodeNetworkConfigSpec{}
			netplanSpec := v1alpha1.NodeNetplanConfigSpec{}
			hash, err := nodeConfigHash(spec, &netplanSpec)
			Expect(err).ToNot(HaveOccurred())
			netplanSpec.DesiredState.Network.Dummies = map[string]netplan.Device{
				"lo.test": {Raw: []byte(`{"addresses":["192.0.2.1/32"]}`)},
			}
			otherHash, err := nodeConfigHash(spec, &netplanSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(otherHash).ToNot(Equal(hash))
			netplanSpec.DesiredState.Network.Dummies["lo.test"] = netplan.Device{Raw: []byte("{")}
			_, err = nodeConfigHash(spec, &netplanSpec)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("label-driven reconciliation", func() {
		const enabled = "enabled"
		var (
			node       *corev1.Node
			revision   v1alpha1.NetworkConfigRevision
			crr        *ConfigRevisionReconciler
			fakeClient client.Client
		)

		BeforeEach(func() {
			configPath, err := filepath.Abs("../../../config/operator/config.yaml")
			Expect(err).ToNot(HaveOccurred())
			GinkgoT().Setenv("OPERATOR_CONFIG", configPath)
			node = makeNode("node1", true)
			node.UID = types.UID(node.Name)
			revision = makeRevision("rev001", false, time.Now())
			revision.UID = types.UID(revision.Name)
			selector := &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "network", Operator: metav1.LabelSelectorOpIn, Values: []string{enabled}},
				},
			}
			revision.Spec.Layer2 = []v1alpha1.Layer2Revision{{
				Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
					ID: 100, VNI: 100100, MTU: 1500, NodeSelector: selector,
				},
			}}
			vni, rt := 100200, "64512:100200"
			revision.Spec.Vrf = []v1alpha1.VRFRevision{{
				VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{
					VRF: "example", VNI: &vni, RouteTarget: &rt, Seq: 10, NodeSelector: selector,
				},
			}}
			fakeClient = fake.NewClientBuilder().WithScheme(testScheme).
				WithObjects(node, &revision).
				WithStatusSubresource(&revision, &v1alpha1.NodeNetworkConfig{}).Build()
			crr = &ConfigRevisionReconciler{
				client: fakeClient, logger: logger, scheme: testScheme,
				vrfConfig: &config.Config{}, apiTimeout: time.Minute, maxUpdating: 1,
				configTimeout: time.Minute, preconfigTimeout: time.Minute,
			}
		})

		It("should add and remove selected Layer2, VRF and Netplan configs without a new revision", func() {
			for _, value := range []string{"", enabled, "disabled", "", enabled} {
				before := &v1alpha1.NodeNetworkConfig{}
				key := client.ObjectKey{Name: node.Name}
				err := fakeClient.Get(context.Background(), key, before)
				if err != nil {
					Expect(client.IgnoreNotFound(err)).To(Succeed())
				}
				node.Labels = map[string]string{}
				if value != "" {
					node.Labels["network"] = value
				}
				Expect(fakeClient.Update(context.Background(), node)).To(Succeed())
				Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
				cfg := &v1alpha1.NodeNetworkConfig{}
				Expect(fakeClient.Get(context.Background(), key, cfg)).To(Succeed())
				Expect(cfg.Spec.Revision).To(Equal(revision.Spec.Revision))
				Expect(cfg.Spec.ConfigHash).ToNot(BeEmpty())
				Expect(cfg.Spec.ConfigUpdateTime).ToNot(BeNil())
				if before.Spec.ConfigHash == cfg.Spec.ConfigHash {
					Expect(cfg.ResourceVersion).To(Equal(before.ResourceVersion))
				}
				netplanCfg := &v1alpha1.NodeNetplanConfig{}
				Expect(fakeClient.Get(context.Background(), key, netplanCfg)).To(Succeed())
				if value == enabled {
					Expect(cfg.Spec.Layer2s).To(HaveKey("100"))
					Expect(cfg.Spec.FabricVRFs).To(HaveKey("example"))
					Expect(netplanCfg.Spec.DesiredState.Network.VLans).To(HaveKey("vlan.100"))
				} else {
					Expect(cfg.Spec.Layer2s).To(BeEmpty())
					Expect(cfg.Spec.FabricVRFs).To(BeEmpty())
					Expect(netplanCfg.Spec.DesiredState.Network.VLans).To(BeEmpty())
				}
				cfg.Status.ConfigStatus = StatusProvisioned
				cfg.Status.LastAppliedRevision = cfg.Spec.Revision
				cfg.Status.LastAppliedConfigHash = cfg.Spec.ConfigHash
				Expect(fakeClient.Status().Update(context.Background(), cfg)).To(Succeed())
				resourceVersion := cfg.ResourceVersion
				Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
				Expect(fakeClient.Get(context.Background(), key, cfg)).To(Succeed())
				Expect(cfg.ResourceVersion).To(Equal(resourceVersion), "unchanged output must not redeploy")
			}

			revisions := &v1alpha1.NetworkConfigRevisionList{}
			Expect(fakeClient.List(context.Background(), revisions)).To(Succeed())
			Expect(revisions.Items).To(HaveLen(1))
		})

		It("should ignore unrelated labels and selector changes with identical output", func() {
			revision.Spec.Layer2[0].NodeSelector.MatchExpressions[0].Values = []string{enabled, "also-enabled"}
			revision.Spec.Vrf[0].NodeSelector = revision.Spec.Layer2[0].NodeSelector
			Expect(fakeClient.Update(context.Background(), &revision)).To(Succeed())
			node.Labels["network"] = enabled
			Expect(fakeClient.Update(context.Background(), node)).To(Succeed())
			Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
			cfg := &v1alpha1.NodeNetworkConfig{}
			key := client.ObjectKey{Name: node.Name}
			Expect(fakeClient.Get(context.Background(), key, cfg)).To(Succeed())
			cfg.Status.ConfigStatus = StatusProvisioned
			cfg.Status.LastAppliedConfigHash = cfg.Spec.ConfigHash
			Expect(fakeClient.Status().Update(context.Background(), cfg)).To(Succeed())
			netplanCfg := &v1alpha1.NodeNetplanConfig{}
			Expect(fakeClient.Get(context.Background(), key, netplanCfg)).To(Succeed())
			configVersion, netplanVersion := cfg.ResourceVersion, netplanCfg.ResourceVersion

			for _, value := range []string{enabled, "also-enabled"} {
				node.Labels["network"] = value
				node.Labels["unrelated"] = value
				Expect(fakeClient.Update(context.Background(), node)).To(Succeed())
				Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
				Expect(fakeClient.Get(context.Background(), key, cfg)).To(Succeed())
				Expect(fakeClient.Get(context.Background(), key, netplanCfg)).To(Succeed())
				Expect(cfg.ResourceVersion).To(Equal(configVersion))
				Expect(netplanCfg.ResourceVersion).To(Equal(netplanVersion))
				Expect(cfg.Status.ConfigStatus).To(Equal(StatusProvisioned))
			}
		})

		It("should queue only the node whose resolved configuration changes", func() {
			other := makeNode("node2", true)
			other.UID = types.UID(other.Name)
			Expect(fakeClient.Create(context.Background(), other)).To(Succeed())
			nodes := map[string]*corev1.Node{node.Name: node, other.Name: other}
			deployments, err := crr.getOutdatedNodes(context.Background(), nodes, nil, &revision)
			Expect(err).ToNot(HaveOccurred())
			Expect(deployments).To(HaveLen(2))
			configs := make([]v1alpha1.NodeNetworkConfig, 0, len(deployments))
			for _, deployment := range deployments {
				configs = append(configs, *deployment.networkConfig)
			}
			node.Labels["network"] = enabled
			deployments, err = crr.getOutdatedNodes(context.Background(), nodes, configs, &revision)
			Expect(err).ToNot(HaveOccurred())
			Expect(deployments).To(HaveLen(1))
			Expect(deployments[0].node.Name).To(Equal(node.Name))
		})

		It("should not queue any nodes without a valid revision", func() {
			deployments, err := crr.getOutdatedNodes(context.Background(), map[string]*corev1.Node{node.Name: node}, nil, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(deployments).To(BeEmpty())
		})

		It("should read one config snapshot for all nodes and reload on the next pass", func() {
			configPath := filepath.Join(GinkgoT().TempDir(), "operator.yaml")
			Expect(os.WriteFile(configPath, []byte("vrfConfig:\n  example:\n    vni: 100300\n    rt: '64512:100300'\n"), 0o600)).To(Succeed())
			GinkgoT().Setenv("OPERATOR_CONFIG", configPath)
			revision.Spec.Vrf[0].VNI = nil
			revision.Spec.Vrf[0].RouteTarget = nil
			revision.Spec.Vrf[0].NodeSelector = nil
			revision.Spec.MirrorSelectors = []v1alpha1.MirrorSelectorRevision{{}}
			other := makeNode("snapshot-node", true)
			other.UID = types.UID(other.Name)
			removed := false
			crr.client = fake.NewClientBuilder().WithScheme(testScheme).
				WithObjects(node, other).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*corev1.NodeList); ok && !removed {
							Expect(os.Remove(configPath)).To(Succeed())
							removed = true
						}
						return c.List(ctx, list, opts...)
					},
				}).Build()
			nodes := map[string]*corev1.Node{node.Name: node, other.Name: other}
			deployments, err := crr.getOutdatedNodes(context.Background(), nodes, nil, &revision)
			Expect(err).ToNot(HaveOccurred())
			Expect(removed).To(BeTrue(), "config file is removed during the first node build")
			Expect(deployments).To(HaveLen(2))
			for _, deployment := range deployments {
				vrf := deployment.networkConfig.Spec.FabricVRFs["example"]
				Expect(vrf.VNI).To(Equal(uint32(100300)))
				Expect(vrf.EVPNImportRouteTargets).To(Equal([]string{"64512:100300"}))
			}
			_, err = crr.getOutdatedNodes(context.Background(), nodes, nil, &revision)
			Expect(err).To(MatchError(ContainSubstring("error loading config snapshot")))
			_, err = crr.CreateNodeNetworkConfig(context.Background(), node, &revision)
			Expect(err).To(MatchError(ContainSubstring("error reloading config")))
		})

		It("should hash repeated builds with multiple imported VRFs identically", func() {
			node.Labels["network"] = enabled
			for _, name := range []string{"extra-a", "extra-b", "extra-c"} {
				vrf := revision.Spec.Vrf[0]
				vrf.VRF = name
				vrf.Import = []v1alpha1.VrfRouteConfigurationPrefixItem{{CIDR: "192.0.2.0/24", Action: permitRoute}}
				revision.Spec.Vrf = append(revision.Spec.Vrf, vrf)
			}
			nodes := map[string]*corev1.Node{node.Name: node}
			deployments, err := crr.getOutdatedNodes(context.Background(), nodes, nil, &revision)
			Expect(err).ToNot(HaveOccurred())
			configs := []v1alpha1.NodeNetworkConfig{*deployments[0].networkConfig}
			for range 20 {
				deployments, err = crr.getOutdatedNodes(context.Background(), nodes, configs, &revision)
				Expect(err).ToNot(HaveOccurred())
				Expect(deployments).To(BeEmpty())
			}
		})

		It("should preserve ready status when only the global revision changes", func() {
			node.Labels["network"] = enabled
			Expect(fakeClient.Update(context.Background(), node)).To(Succeed())
			Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
			cfg := &v1alpha1.NodeNetworkConfig{}
			key := client.ObjectKey{Name: node.Name}
			Expect(fakeClient.Get(context.Background(), key, cfg)).To(Succeed())
			hash := cfg.Spec.ConfigHash
			cfg.Status.ConfigStatus = StatusProvisioned
			cfg.Status.LastAppliedRevision = cfg.Spec.Revision
			cfg.Status.LastAppliedConfigHash = hash
			Expect(fakeClient.Status().Update(context.Background(), cfg)).To(Succeed())

			next := makeRevision("rev002", false, time.Now().Add(time.Minute))
			next.UID = types.UID(next.Name)
			next.Spec.Layer2, next.Spec.Vrf = revision.Spec.Layer2, revision.Spec.Vrf
			Expect(fakeClient.Create(context.Background(), &next)).To(Succeed())
			Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
			Expect(fakeClient.Get(context.Background(), key, cfg)).To(Succeed())
			Expect(cfg.Spec.Revision).To(Equal(next.Spec.Revision))
			Expect(cfg.Spec.ConfigHash).To(Equal(hash))
			Expect(cfg.Status.ConfigStatus).To(Equal(StatusProvisioned))
			netplanCfg := &v1alpha1.NodeNetplanConfig{}
			Expect(fakeClient.Get(context.Background(), key, netplanCfg)).To(Succeed())
			Expect(netplanCfg.OwnerReferences).To(ContainElement(HaveField("UID", next.UID)))
		})

		It("should recover a failed node-local update when labels are corrected", func() {
			Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
			key := client.ObjectKey{Name: node.Name}
			cfg := &v1alpha1.NodeNetworkConfig{}
			Expect(fakeClient.Get(context.Background(), key, cfg)).To(Succeed())
			originalHash := cfg.Spec.ConfigHash
			cfg.Status.ConfigStatus = StatusProvisioned
			cfg.Status.LastAppliedRevision = cfg.Spec.Revision
			cfg.Status.LastAppliedConfigHash = originalHash
			Expect(fakeClient.Status().Update(context.Background(), cfg)).To(Succeed())
			node.Labels["network"] = enabled
			Expect(fakeClient.Update(context.Background(), node)).To(Succeed())
			Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
			Expect(fakeClient.Get(context.Background(), key, cfg)).To(Succeed())
			Expect(cfg.Spec.ConfigHash).ToNot(Equal(originalHash))
			cfg.Status.ConfigStatus = StatusInvalid
			cfg.Status.LastAppliedConfigHash = cfg.Spec.ConfigHash
			Expect(fakeClient.Status().Update(context.Background(), cfg)).To(Succeed())
			delete(node.Labels, "network")
			Expect(fakeClient.Update(context.Background(), node)).To(Succeed())

			Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
			Expect(fakeClient.Get(context.Background(), key, cfg)).To(Succeed())
			Expect(cfg.Spec.ConfigHash).To(Equal(originalHash))
			Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(&revision), &revision)).To(Succeed())
			Expect(revision.Status.IsInvalid).To(BeFalse())
		})

		It("should respect the rollout concurrency limit for label changes", func() {
			node.Labels["network"] = enabled
			Expect(fakeClient.Update(context.Background(), node)).To(Succeed())
			cfg := makeNodeConfig("node1", "rev001", StatusProvisioning, time.Now())
			cfg.Spec.ConfigHash = previousConfigHash
			cfg.OwnerReferences = []metav1.OwnerReference{{Name: node.Name}, {Name: revision.Name}}
			Expect(fakeClient.Create(context.Background(), &cfg)).To(Succeed())
			Expect(fakeClient.Status().Update(context.Background(), &cfg)).To(Succeed())
			Expect(crr.reconcileDebounced(context.Background())).To(Succeed())
			Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(&cfg), &cfg)).To(Succeed())
			Expect(cfg.Spec.ConfigHash).To(Equal(previousConfigHash))
			Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(&revision), &revision)).To(Succeed())
			Expect(revision.Status.Ongoing).To(Equal(1))
			Expect(revision.Status.Queued).To(Equal(1))
		})
	})

	Describe("removeRedundantConfigs", func() {
		It("should delete configs with fewer than 2 owner references", func() {
			cfg1 := makeNodeConfig("node1", "rev001", StatusProvisioned, time.Now())
			cfg1.OwnerReferences = []metav1.OwnerReference{
				{Name: "revision1"},
				{Name: "node1"},
			}
			cfg2 := makeNodeConfig("node2", "rev001", StatusProvisioned, time.Now())
			// cfg2 has only 1 owner ref - should be deleted
			cfg2.OwnerReferences = []metav1.OwnerReference{
				{Name: "revision1"},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(&cfg1, &cfg2).
				Build()

			crr := &ConfigRevisionReconciler{
				logger: logger,
				client: fakeClient,
			}

			result, err := crr.removeRedundantConfigs(context.Background(), []v1alpha1.NodeNetworkConfig{cfg1, cfg2})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result[0].Name).To(Equal("node1"))
		})

		It("should retain configs with 2 or more owner references", func() {
			cfg := makeNodeConfig("node1", "rev001", StatusProvisioned, time.Now())
			cfg.OwnerReferences = []metav1.OwnerReference{
				{Name: "revision1"},
				{Name: "node1"},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(&cfg).
				Build()

			crr := &ConfigRevisionReconciler{
				logger: logger,
				client: fakeClient,
			}

			result, err := crr.removeRedundantConfigs(context.Background(), []v1alpha1.NodeNetworkConfig{cfg})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(1))
		})
	})

	Describe("countReferences", func() {
		It("should count configs matching a revision's hash", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", StatusProvisioned, time.Now()),
				makeNodeConfig("node2", "rev001", StatusProvisioned, time.Now()),
				makeNodeConfig("node3", "rev002", StatusProvisioned, time.Now()),
			}
			Expect(countReferences(&revision, configs)).To(Equal(2))
		})

		It("should return 0 when no configs match", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev002", StatusProvisioned, time.Now()),
			}
			Expect(countReferences(&revision, configs)).To(Equal(0))
		})
	})

	Describe("matchSelector", func() {
		It("should return true when selector is nil", func() {
			node := makeNode("node1", true)
			Expect(matchSelector(node, nil)).To(BeTrue())
		})

		It("should return true when node labels match", func() {
			node := makeNode("node1", true)
			node.Labels = map[string]string{"role": "worker"}
			selector := &metav1.LabelSelector{
				MatchLabels: map[string]string{"role": "worker"},
			}
			Expect(matchSelector(node, selector)).To(BeTrue())
		})

		It("should return false when node labels do not match", func() {
			node := makeNode("node1", true)
			node.Labels = map[string]string{"role": "master"}
			selector := &metav1.LabelSelector{
				MatchLabels: map[string]string{"role": "worker"},
			}
			Expect(matchSelector(node, selector)).To(BeFalse())
		})
	})

	Describe("convertSelector", func() {
		It("should convert matchLabels to a selector", func() {
			sel, err := convertSelector(map[string]string{"env": "prod"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(sel).ToNot(BeNil())
		})

		It("should convert matchExpressions to a selector", func() {
			exprs := []metav1.LabelSelectorRequirement{
				{
					Key:      "env",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"prod", "staging"},
				},
			}
			sel, err := convertSelector(nil, exprs)
			Expect(err).ToNot(HaveOccurred())
			Expect(sel).ToNot(BeNil())
		})

		It("should combine matchLabels and matchExpressions", func() {
			exprs := []metav1.LabelSelectorRequirement{
				{
					Key:      "zone",
					Operator: metav1.LabelSelectorOpExists,
				},
			}
			sel, err := convertSelector(map[string]string{"env": "prod"}, exprs)
			Expect(err).ToNot(HaveOccurred())
			Expect(sel).ToNot(BeNil())
		})
	})

	Describe("listNodes", func() {
		It("should only include ready nodes", func() {
			readyNode := makeNode("ready-node", true)
			notReadyNode := makeNode("not-ready-node", false)

			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(readyNode, notReadyNode).
				Build()

			result, err := listNodes(context.Background(), fakeClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(1))
			_, ok := result["ready-node"]
			Expect(ok).To(BeTrue())
		})

		It("should exclude nodes with no Ready condition", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "no-condition-node"},
				Status:     corev1.NodeStatus{},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(node).
				Build()

			result, err := listNodes(context.Background(), fakeClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeEmpty())
		})
	})

	Describe("invalidateRevision", func() {
		It("should set IsInvalid to true and update status", func() {
			revision := makeRevision("rev001", false, time.Now())
			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(&revision).
				WithStatusSubresource(&revision).
				Build()

			crr := &ConfigRevisionReconciler{
				logger: logger,
				client: fakeClient,
			}

			failedAt := metav1.NewTime(time.Now().Truncate(time.Second))
			err := crr.invalidateRevision(context.Background(), &revision, "node1", "test reason", failedAt)
			Expect(err).ToNot(HaveOccurred())
			Expect(revision.Status.IsInvalid).To(BeTrue())
			Expect(revision.Status.FailedNode).To(Equal("node1"))
			Expect(revision.Status.FailedMessage).To(Equal("test reason"))
			Expect(revision.Status.FailedAt).To(Equal(&failedAt))
		})
	})

	Describe("updateRevisionCounters", func() {
		It("should update status counters for each revision", func() {
			rev1 := makeRevision("rev001", false, time.Now())
			rev2 := makeRevision("rev002", false, time.Now().Add(-time.Minute))

			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(&rev1, &rev2).
				WithStatusSubresource(&rev1, &rev2).
				Build()

			crr := &ConfigRevisionReconciler{
				logger: logger,
				client: fakeClient,
			}

			cntMap := map[string]*counters{
				"rev001": {ready: 3, ongoing: 1, invalid: 0},
				"rev002": {ready: 0, ongoing: 0, invalid: 0},
			}

			err := crr.updateRevisionCounters(
				context.Background(),
				[]v1alpha1.NetworkConfigRevision{rev1, rev2},
				&rev1, // currentRevision
				2,     // queued
				5,     // totalNodes
				cntMap,
			)
			Expect(err).ToNot(HaveOccurred())

			// Verify the status counters were persisted for rev1 (current revision gets queued=2)
			updated1 := &v1alpha1.NetworkConfigRevision{}
			Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: rev1.Name}, updated1)).To(Succeed())
			Expect(updated1.Status.Ready).To(Equal(3))
			Expect(updated1.Status.Ongoing).To(Equal(1))
			Expect(updated1.Status.Queued).To(Equal(2))
			Expect(updated1.Status.Total).To(Equal(5))

			// rev2 is not the current revision — queued stays 0
			updated2 := &v1alpha1.NetworkConfigRevision{}
			Expect(fakeClient.Get(context.Background(), types.NamespacedName{Name: rev2.Name}, updated2)).To(Succeed())
			Expect(updated2.Status.Ready).To(Equal(0))
			Expect(updated2.Status.Ongoing).To(Equal(0))
			Expect(updated2.Status.Queued).To(Equal(0))
			Expect(updated2.Status.Total).To(Equal(5))
		})
	})
})

// makeNodeConfig creates a NodeNetworkConfig for testing.
func makeNodeConfig(name, revision, status string, lastUpdate time.Time) v1alpha1.NodeNetworkConfig {
	return v1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1alpha1.NodeNetworkConfigSpec{
			Revision: revision,
		},
		Status: v1alpha1.NodeNetworkConfigStatus{
			ConfigStatus: status,
			LastUpdate:   metav1.NewTime(lastUpdate),
		},
	}
}

// makeNode creates a corev1.Node for testing.
func makeNode(name string, ready bool) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: status,
				},
			},
		},
	}
}
