package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Intent-based LoadBalancer Service (Tier 2).
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

		By("Applying intent base configs (VRFs, Networks, Destinations)")
		baseCfg, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, baseCfg)).To(Succeed())

		By("Applying intent LB Inbound manifest")
		lbManifest, err := readTestdata("intent/lb/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, lbManifest)).To(Succeed())

		By("Applying intent LB app manifests (Deployment, Service)")
		app, err := readTestdata("intent/lb/app.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, app, ns)).To(Succeed())

		By("Applying macvlan NAD for VLAN 501")
		nad501, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad501, ns)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "macvlan-intent-lb")

		app, _ := readTestdata("intent/lb/app.yaml")
		_ = f.DeleteManifestInNamespace(ctx, app, ns)

		lbManifest, _ := readTestdata("intent/lb/manifests.yaml")
		_ = f.DeleteManifest(ctx, lbManifest)
	})

	Context("m2m VRF LB via Inbound CRD", func() {
		It("should be reachable via LoadBalancer VIP from a macvlan pod", func() {
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
					if ingress.IP != "" {
						lbIPv4 = ingress.IP
					}
				}
				if lbIPv4 == "" {
					return fmt.Errorf("LB IP not ready yet (v4=%q)", lbIPv4)
				}
				return nil
			}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

			By("Creating macvlan-intent-lb on worker-1 (VLAN 501, m2m)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-intent-lb", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan501", "ips": ["%s/24"]}]`,
					cfg.Macvlan02IPv4),
			})).To(Succeed())

			Expect(f.WaitForPodReady(ctx, ns, "macvlan-intent-lb", cfg.PodReadyTimeout)).To(Succeed())

			By("Waiting for BGP route propagation")
			time.Sleep(10 * time.Second)

			By(fmt.Sprintf("Verifying macvlan pod can curl LB VIP %s (IPv4)", lbIPv4))
			Eventually(func() string {
				code, _ := f.CurlFromPod(ctx, ns, "macvlan-intent-lb",
					fmt.Sprintf("http://%s:80", lbIPv4))
				return code
			}).WithTimeout(cfg.BGPTimeout).WithPolling(5 * time.Second).Should(
				Equal("200"), "Expected HTTP 200 from intent LB VIP IPv4")
		})
	})
})
