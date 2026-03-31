package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Intent-Exclusive Egress NAT test using Outbound CRD.
// Requires E2E_INTENT_MODE=true — intent reconciler produces NNCs, not legacy.
// Validates that the Outbound CRD correctly creates policy routes, local VRFs,
// and cluster VRF entries for Coil egress when intent is the sole NNC producer.
var _ = Describe("Intent-Exclusive: Egress NAT", Label("intent-exclusive", "egress"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  string
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		Expect(f.IsIntentMode()).To(BeTrue(), "intent-exclusive tests require E2E_INTENT_MODE=true")
		ctx = context.Background()
		ns = "e2e-test-intent-excl-egress"

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying intent base configs (VRFs, Networks, Destinations)")
		baseManifest, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, baseManifest)).To(Succeed())

		By("Applying intent Outbound egress manifests")
		manifest, err := readTestdata("intent/egress/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

		By("Applying Coil egress + test pod (intent-exclusive)")
		egressManifest, err := readTestdata("intent/egress/coil-egress-exclusive.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, egressManifest, ns)).To(Succeed())

		By("Waiting for intent reconciler to process")
		time.Sleep(10 * time.Second)
	})

	AfterEach(func() {
		egressManifest, _ := readTestdata("intent/egress/coil-egress-exclusive.yaml")
		_ = f.DeleteManifestInNamespace(ctx, egressManifest, ns)
		manifest, _ := readTestdata("intent/egress/manifests.yaml")
		_ = f.DeleteManifest(ctx, manifest)
	})

	Context("m2m intent egress", func() {
		It("should NAT egress traffic to m2mgw through intent-based Outbound", func() {
			cfg := f.Config

			By("Waiting for Coil egress gateway to be provisioned")
			Eventually(func() bool {
				pods, err := f.KubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
				if err != nil {
					return false
				}
				for _, pod := range pods.Items {
					// Coil egress gateway pods are named after the Egress CR
					if pod.Name != "egress-excl-01" {
						for _, cs := range pod.Status.ContainerStatuses {
							if cs.Ready {
								return true
							}
						}
					}
				}
				return false
			}).WithTimeout(3*time.Minute).WithPolling(5*time.Second).Should(BeTrue(),
				"Coil egress gateway should be running in namespace")

			By("Waiting for egress pod to be ready")
			Expect(f.WaitForPodReady(ctx, ns, "egress-excl-01", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying egress-excl-01 can reach m2mgw (IPv4)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "egress-excl-01", cfg.M2MGWIPv4, 3)
				return r != nil && r.Success
			}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"Intent-exclusive: Egress pod should reach m2mgw IPv4 via intent pipeline")

			By("Verifying egress-excl-01 can reach m2mgw (IPv6)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "egress-excl-01", cfg.M2MGWIPv6, 3)
				return r != nil && r.Success
			}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"Intent-exclusive: Egress pod should reach m2mgw IPv6 via intent pipeline")

			By("Verifying egress-excl-01 CANNOT reach c2mgw (wrong VRF, IPv4)")
			Expect(f.AssertNoConnectivity(ctx, ns, "egress-excl-01", cfg.C2MGWIPv4)).To(Succeed())

			By("Verifying egress-excl-01 CANNOT reach c2mgw (wrong VRF, IPv6)")
			Expect(f.AssertNoConnectivity(ctx, ns, "egress-excl-01", cfg.C2MGWIPv6)).To(Succeed())
		})
	})
})
