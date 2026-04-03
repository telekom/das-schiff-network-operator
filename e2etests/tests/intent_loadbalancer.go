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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Intent-based LoadBalancer Service (Tier 2).
// Validates that the intent CRD pipeline does not break LB functionality.
// Uses CurlFromCluster2Pod (DCGW) to verify VIP reachability, matching
// the existing LB test pattern.
var _ = Describe("Intent LoadBalancer Service", Label("intent", "lb"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-intent-lb"
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
		baseCfg, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, baseCfg)).To(Succeed())

		By("Applying intent LB Inbound manifest")
		lbManifest, err := readTestdata("intent/lb/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, lbManifest)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifest(context.Background(), lbManifest)
		})

		By("Waiting for MetalLB IPAddressPool to be created by platform controller")
		Eventually(func() error {
			pool := &unstructured.Unstructured{}
			pool.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "metallb.io",
				Version: "v1beta1",
				Kind:    "IPAddressPool",
			})
			return f.DynamicGet(ctx, "metallb-system", "ib-lb", pool)
		}).WithTimeout(60 * time.Second).WithPolling(5 * time.Second).Should(Succeed(),
			"MetalLB IPAddressPool should be created by platform-metallb controller")

		By("Applying intent LB app manifests (Deployment, Service)")
		app, err := readTestdata("intent/lb/app.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, app, ns)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifestInNamespace(context.Background(), app, ns)
		})
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
				Equal("200"), "Expected HTTP 200 from intent LB VIP via m2mgw")
		})
	})
})
