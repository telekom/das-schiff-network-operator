package operator

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("ConfigRevisionReconciler helpers", func() {
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
			ready, ongoing, invalid := crr.getRevisionCounters(configs, &revision)
			Expect(ready).To(Equal(2))
			Expect(ongoing).To(Equal(0))
			Expect(invalid).To(Equal(0))
		})

		It("should count provisioning nodes correctly", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", StatusProvisioning, time.Now()),
			}
			ready, ongoing, invalid := crr.getRevisionCounters(configs, &revision)
			Expect(ready).To(Equal(0))
			Expect(ongoing).To(Equal(1))
			Expect(invalid).To(Equal(0))
		})

		It("should count invalid nodes correctly", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", StatusInvalid, time.Now().Add(-time.Minute)),
			}
			ready, ongoing, invalid := crr.getRevisionCounters(configs, &revision)
			Expect(ready).To(Equal(0))
			Expect(ongoing).To(Equal(0))
			Expect(invalid).To(Equal(1))
		})

		It("should count nodes with empty status as ongoing (pre-config)", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", "", time.Now()),
			}
			ready, ongoing, invalid := crr.getRevisionCounters(configs, &revision)
			Expect(ready).To(Equal(0))
			Expect(ongoing).To(Equal(1))
			Expect(invalid).To(Equal(0))
		})

		It("should count timed-out provisioning config as invalid (still ongoing)", func() {
			crr.configTimeout = 50 * time.Millisecond
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", StatusProvisioning, time.Now().Add(-time.Minute)),
			}
			ready, ongoing, invalid := crr.getRevisionCounters(configs, &revision)
			Expect(ongoing).To(Equal(1)) // still counted as ongoing
			Expect(invalid).To(Equal(1)) // also counted as invalid because timeout reached
			Expect(ready).To(Equal(0))
		})

		It("should not count configs for other revisions", func() {
			revision := makeRevision("rev001", false, time.Now())
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev002", StatusProvisioned, time.Now().Add(-time.Minute)),
			}
			ready, ongoing, invalid := crr.getRevisionCounters(configs, &revision)
			Expect(ready).To(Equal(0))
			Expect(ongoing).To(Equal(0))
			Expect(invalid).To(Equal(0))
		})
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

	Describe("getOutdatedNodes", func() {
		It("should return empty result when revision is nil", func() {
			nodes := map[string]*corev1.Node{
				"node1": makeNode("node1", true),
			}
			result := getOutdatedNodes(nodes, nil, nil)
			Expect(result).To(BeEmpty())
		})

		It("should exclude nodes that already have matching config", func() {
			revision := makeRevision("rev001", false, time.Now())
			nodes := map[string]*corev1.Node{
				"node1": makeNode("node1", true),
				"node2": makeNode("node2", true),
			}
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev001", StatusProvisioned, time.Now()),
			}
			result := getOutdatedNodes(nodes, configs, &revision)
			Expect(result).To(HaveLen(1))
			Expect(result[0].Name).To(Equal("node2"))
		})

		It("should return all nodes when no configs match revision", func() {
			revision := makeRevision("rev001", false, time.Now())
			nodes := map[string]*corev1.Node{
				"node1": makeNode("node1", true),
			}
			configs := []v1alpha1.NodeNetworkConfig{
				makeNodeConfig("node1", "rev000", StatusProvisioned, time.Now()),
			}
			result := getOutdatedNodes(nodes, configs, &revision)
			Expect(result).To(HaveLen(1))
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

			err := crr.invalidateRevision(context.Background(), &revision, "test reason")
			Expect(err).ToNot(HaveOccurred())
			Expect(revision.Status.IsInvalid).To(BeTrue())
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
