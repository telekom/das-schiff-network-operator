package tests

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Intent-Exclusive LoadBalancer Service.
// Requires E2E_INTENT_MODE=true — intent reconciler produces NNCs, not legacy.
// Validates that the Inbound CRD + MetalLB integration works end-to-end when
// the intent pipeline is the sole NNC producer.
var _ = Describe("Intent-Exclusive: LoadBalancer Service", Label("intent-exclusive", "lb"), func() {
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
		ns = "e2e-test-intent-excl-lb"

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteNamespace(context.Background(), ns)
		})

		By("Applying intent base configs (VRFs, Networks, Destinations)")
		baseCfg, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, baseCfg)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifest(context.Background(), baseCfg)
		})

		By("Applying intent LB Inbound manifest")
		lbManifest, err := readTestdata("intent/lb/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, lbManifest)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifest(context.Background(), lbManifest)
		})

		By("Applying MetalLB m2m pool configuration")
		metallb, err := readTestdata("lb-service/metallb.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, metallb)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifest(context.Background(), metallb)
		})

		By("Applying intent LB app manifests (Deployment, Service)")
		app, err := readTestdata("intent/lb/app.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, app, ns)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifestInNamespace(context.Background(), app, ns)
		})

		By("Waiting for intent reconciler to process")
		time.Sleep(10 * time.Second)
	})

	Context("m2m VRF LB via Inbound CRD", func() {
		It("should be reachable via LoadBalancer VIP from DCGW", func() {
			cfg := f.Config

			By("Waiting for intent-lb-app pods to be ready")
			Eventually(func() error {
				pods, err := f.KubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
					LabelSelector: "app=intent-lb-app",
				})
				if err != nil {
					return err
				}
				readyCount := 0
				for _, pod := range pods.Items {
					for _, cs := range pod.Status.ContainerStatuses {
						if cs.Ready {
							readyCount++
						}
					}
				}
				if readyCount < 2 {
					return fmt.Errorf("only %d/2 pods ready", readyCount)
				}
				return nil
			}).WithTimeout(cfg.PodReadyTimeout).WithPolling(5 * time.Second).Should(Succeed())

			By("Waiting for Service to get LoadBalancer IP")
			var lbIPv4 string
			Eventually(func() error {
				svc, err := f.KubeClient.CoreV1().Services(ns).Get(ctx, "intent-lb-svc", metav1.GetOptions{})
				if err != nil {
					return err
				}
				for _, ingress := range svc.Status.LoadBalancer.Ingress {
					if ingress.IP != "" && !strings.Contains(ingress.IP, ":") {
						lbIPv4 = ingress.IP
					}
				}
				if lbIPv4 == "" {
					return fmt.Errorf("LB IPv4 not ready yet")
				}
				return nil
			}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

			By("Waiting for route convergence")
			time.Sleep(15 * time.Second)

			By(fmt.Sprintf("Verifying m2mgw can curl LB VIP %s (IPv4)", lbIPv4))
			Eventually(func() string {
				code, _ := f.CurlFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway",
					fmt.Sprintf("http://%s:80", lbIPv4))
				return code
			}).WithTimeout(cfg.BGPTimeout).WithPolling(5 * time.Second).Should(
				Equal("200"), "Intent-exclusive: Expected HTTP 200 from LB VIP via intent pipeline")
		})
	})
})
