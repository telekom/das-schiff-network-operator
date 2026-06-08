package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent-based Egress NAT test using Outbound CRD.
// Validates that intent CRDs don't break Coil egress functionality.
var _ = Describe("Intent Egress NAT", Label("intent", "egress"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-intent-egress"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteNamespace(context.Background(), ns)
		})

		By("Applying intent base configs (VRFs, Networks, Destinations)")
		baseManifest, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, baseManifest)).To(Succeed())

		By("Applying intent Outbound egress manifests in default namespace")
		manifest, err := readTestdata("intent/egress/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		// Outbound must be in the default namespace where the intent reconciler watches.
		Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifest(context.Background(), manifest)
		})

		By("Waiting for Coil Egress to be created by platform controller")
		Eventually(func() error {
			egress := &unstructured.Unstructured{}
			egress.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "coil.cybozu.com",
				Version: "v2",
				Kind:    "Egress",
			})
			return f.DynamicGet(ctx, "default", "ob-egress", egress)
		}).WithTimeout(120*time.Second).WithPolling(5*time.Second).Should(Succeed(),
			"Coil Egress should be created by platform-coil controller")

		By("Waiting for Coil egress gateway pod to be ready")
		Eventually(func() bool {
			pods, err := f.KubeClient.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
			if err != nil {
				return false
			}
			for _, pod := range pods.Items {
				if pod.Labels != nil && pod.Labels["app.kubernetes.io/managed-by"] == "network-connector" {
					continue
				}
				if pod.Name == "egress-intent-01" {
					continue
				}
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.Ready {
						return true
					}
				}
			}
			return false
		}).WithTimeout(3*time.Minute).WithPolling(5*time.Second).Should(BeTrue(),
			"Coil egress gateway should be running")

		By("Creating egress test pod with Coil annotation")
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "egress-intent-01",
				Namespace: ns,
				Annotations: map[string]string{
					"egress.coil.cybozu.com/default": "ob-egress",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:    "tester",
						Image:   "busybox:1.37",
						Command: []string{"sleep", "infinity"},
					},
				},
				RestartPolicy: corev1.RestartPolicyNever,
			},
		}
		Expect(f.Client.Create(ctx, pod)).To(Succeed())
		DeferCleanup(func() {
			_ = f.Client.Delete(context.Background(), pod)
		})
	})

	Context("m2m intent egress", func() {
		It("should NAT egress traffic to m2mgw through intent-based Outbound", func() {
			cfg := f.Config

			By("Waiting for egress pod to be ready")
			Expect(f.WaitForPodReady(ctx, ns, "egress-intent-01", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying egress-intent-01 can reach m2mgw (IPv4)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "egress-intent-01", cfg.M2MGWIPv4, 3)
				return r != nil && r.Success
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"Intent egress pod cannot reach m2mgw IPv4")

			By("Verifying egress-intent-01 can reach m2mgw (IPv6)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "egress-intent-01", cfg.M2MGWIPv6, 3)
				return r != nil && r.Success
			}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"Intent egress pod cannot reach m2mgw IPv6")

			By("Verifying egress-intent-01 CANNOT reach c2mgw (wrong VRF, IPv4)")
			Expect(f.AssertNoConnectivity(ctx, ns, "egress-intent-01", cfg.C2MGWIPv4)).To(Succeed())

			By("Verifying egress-intent-01 CANNOT reach c2mgw (wrong VRF, IPv6)")
			Expect(f.AssertNoConnectivity(ctx, ns, "egress-intent-01", cfg.C2MGWIPv6)).To(Succeed())
		})
	})
})
