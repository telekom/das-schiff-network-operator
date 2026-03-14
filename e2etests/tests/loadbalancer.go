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

// TC-06: LoadBalancer Service.
var _ = Describe("LoadBalancer Service", Label("lb"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-lb"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())
	})

	Context("m2m VRF (TC-06)", func() {
		BeforeEach(func() {
			By("Applying MetalLB m2m pool configuration")
			metallb, err := readTestdata("lb-service/metallb.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifest(ctx, metallb)).To(Succeed())

			By("Applying m2m LB app manifests (Deployment, Service)")
			app, err := readTestdata("lb-service/app.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifestInNamespace(ctx, app, ns)).To(Succeed())
		})

		AfterEach(func() {
			app, _ := readTestdata("lb-service/app.yaml")
			_ = f.DeleteManifest(ctx, app)
		})

		It("should be reachable via LoadBalancer VIP from m2mgw", func() {
			cfg := f.Config

			By("Waiting for podinfo pods to be ready")
			Eventually(func() error {
				pods, err := f.KubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
					LabelSelector: "app=podinfo",
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

			By("Waiting for Service to get LoadBalancer IPs")
			var lbIPv4, lbIPv6 string
			Eventually(func() error {
				svc, err := f.KubeClient.CoreV1().Services(ns).Get(ctx, "podinfo-lb", metav1.GetOptions{})
				if err != nil {
					return err
				}
				for _, ingress := range svc.Status.LoadBalancer.Ingress {
					if ingress.IP == "" {
						continue
					}
					if strings.Contains(ingress.IP, ":") {
						lbIPv6 = ingress.IP
					} else {
						lbIPv4 = ingress.IP
					}
				}
				if lbIPv4 == "" || lbIPv6 == "" {
					return fmt.Errorf("LB IPs not ready yet (v4=%q v6=%q)", lbIPv4, lbIPv6)
				}
				return nil
			}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

			By(fmt.Sprintf("Verifying m2mgw can curl LB VIP %s (IPv4)", lbIPv4))
			statusCode, err := f.CurlFromContainer(ctx, cfg.ClabM2MGW,
				fmt.Sprintf("http://%s:80", lbIPv4))
			Expect(err).NotTo(HaveOccurred())
			Expect(statusCode).To(Equal("200"), "Expected HTTP 200 from LB VIP IPv4")

			By(fmt.Sprintf("Verifying m2mgw can curl LB VIP %s (IPv6)", lbIPv6))
			statusCode, err = f.CurlFromContainer(ctx, cfg.ClabM2MGW,
				fmt.Sprintf("http://[%s]:80", lbIPv6))
			Expect(err).NotTo(HaveOccurred())
			Expect(statusCode).To(Equal("200"), "Expected HTTP 200 from LB VIP IPv6")

			By("Verifying c2mgw CANNOT reach m2m LB VIP (cross-VRF isolation, IPv4)")
			_, err = f.CurlFromContainer(ctx, cfg.ClabC2MGW,
				fmt.Sprintf("http://%s:80", lbIPv4))
			Expect(err).To(HaveOccurred(), "c2mgw should NOT reach m2m LB VIP IPv4")

			By("Verifying c2mgw CANNOT reach m2m LB VIP (cross-VRF isolation, IPv6)")
			_, err = f.CurlFromContainer(ctx, cfg.ClabC2MGW,
				fmt.Sprintf("http://[%s]:80", lbIPv6))
			Expect(err).To(HaveOccurred(), "c2mgw should NOT reach m2m LB VIP IPv6")
		})
	})

	Context("c2m VRF (TC-06b)", func() {
		BeforeEach(func() {
			By("Applying MetalLB c2m pool configuration")
			metallb, err := readTestdata("lb-service/metallb-c2m.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifest(ctx, metallb)).To(Succeed())

			By("Applying c2m LB app manifests (Deployment, Service)")
			app, err := readTestdata("lb-service/app-c2m.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifestInNamespace(ctx, app, ns)).To(Succeed())
		})

		AfterEach(func() {
			app, _ := readTestdata("lb-service/app-c2m.yaml")
			_ = f.DeleteManifest(ctx, app)
		})

		It("should be reachable via LoadBalancer VIP from c2mgw", func() {
			cfg := f.Config

			By("Waiting for podinfo-c2m pods to be ready")
			Eventually(func() error {
				pods, err := f.KubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
					LabelSelector: "app=podinfo-c2m",
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
					return fmt.Errorf("only %d/2 c2m pods ready", readyCount)
				}
				return nil
			}).WithTimeout(cfg.PodReadyTimeout).WithPolling(5 * time.Second).Should(Succeed())

			By("Waiting for c2m Service to get LoadBalancer IPs")
			var lbIPv4, lbIPv6 string
			Eventually(func() error {
				svc, err := f.KubeClient.CoreV1().Services(ns).Get(ctx, "podinfo-c2m-lb", metav1.GetOptions{})
				if err != nil {
					return err
				}
				for _, ingress := range svc.Status.LoadBalancer.Ingress {
					if ingress.IP == "" {
						continue
					}
					if strings.Contains(ingress.IP, ":") {
						lbIPv6 = ingress.IP
					} else {
						lbIPv4 = ingress.IP
					}
				}
				if lbIPv4 == "" || lbIPv6 == "" {
					return fmt.Errorf("c2m LB IPs not ready yet (v4=%q v6=%q)", lbIPv4, lbIPv6)
				}
				return nil
			}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

			By(fmt.Sprintf("Verifying c2mgw can curl c2m LB VIP %s (IPv4)", lbIPv4))
			statusCode, err := f.CurlFromContainer(ctx, cfg.ClabC2MGW,
				fmt.Sprintf("http://%s:80", lbIPv4))
			Expect(err).NotTo(HaveOccurred())
			Expect(statusCode).To(Equal("200"), "Expected HTTP 200 from c2m LB VIP IPv4")

			By(fmt.Sprintf("Verifying c2mgw can curl c2m LB VIP %s (IPv6)", lbIPv6))
			statusCode, err = f.CurlFromContainer(ctx, cfg.ClabC2MGW,
				fmt.Sprintf("http://[%s]:80", lbIPv6))
			Expect(err).NotTo(HaveOccurred())
			Expect(statusCode).To(Equal("200"), "Expected HTTP 200 from c2m LB VIP IPv6")

			By("Verifying m2mgw CANNOT reach c2m LB VIP (cross-VRF isolation, IPv4)")
			_, err = f.CurlFromContainer(ctx, cfg.ClabM2MGW,
				fmt.Sprintf("http://%s:80", lbIPv4))
			Expect(err).To(HaveOccurred(), "m2mgw should NOT reach c2m LB VIP IPv4")

			By("Verifying m2mgw CANNOT reach c2m LB VIP (cross-VRF isolation, IPv6)")
			_, err = f.CurlFromContainer(ctx, cfg.ClabM2MGW,
				fmt.Sprintf("http://[%s]:80", lbIPv6))
			Expect(err).To(HaveOccurred(), "m2mgw should NOT reach c2m LB VIP IPv6")
		})
	})
})
