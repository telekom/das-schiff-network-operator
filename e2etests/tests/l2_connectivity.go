// Package tests contains all E2E test cases.
// Each file registers Describe blocks that are picked up by the suite.
package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-01: L2 Connectivity (same VLAN)
var _ = Describe("L2 Connectivity", Label("l2", "smoke"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-l2"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying L2 NADs")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())
	})

	AfterEach(func() {
		By("Cleaning up test pods")
		_ = f.DeletePod(ctx, ns, "macvlan-01")
		_ = f.DeletePod(ctx, ns, "macvlan-02")
	})

	It("should allow ping between pods on the same VLAN across nodes", func() {
		cfg := f.Config

		By("Creating macvlan-01 on worker-1 (VLAN 501, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "macvlan-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		})).To(Succeed())

		By("Creating macvlan-02 on worker-2 (VLAN 501, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "macvlan-02", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan02IPv4, cfg.Macvlan02IPv6),
		})).To(Succeed())

		By("Waiting for pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "macvlan-01", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "macvlan-02", cfg.PodReadyTimeout)).To(Succeed())

		By("Verifying IPv4 connectivity: macvlan-01 → macvlan-02")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "macvlan-01", cfg.Macvlan02IPv4, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("IPv4 ping failed: %s\n", result.Output)
			}
			return result != nil && result.Success
		}).WithTimeout(60*time.Second).WithPolling(3*time.Second).Should(BeTrue(), "IPv4 ping failed")

		By("Verifying IPv6 connectivity: macvlan-01 → macvlan-02")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "macvlan-01", cfg.Macvlan02IPv6, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("IPv6 ping failed: %s\n", result.Output)
			}
			return result != nil && result.Success
		}).WithTimeout(30*time.Second).WithPolling(3*time.Second).Should(BeTrue(), "IPv6 ping failed")

		By("Verifying IPv4 connectivity: macvlan-02 → macvlan-01")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "macvlan-02", cfg.Macvlan01IPv4, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("Reverse IPv4 ping failed: %s\n", result.Output)
			}
			return result != nil && result.Success
		}).WithTimeout(60*time.Second).WithPolling(3*time.Second).Should(BeTrue(), "Reverse IPv4 ping failed")

		By("Verifying IPv6 connectivity: macvlan-02 → macvlan-01")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "macvlan-02", cfg.Macvlan01IPv6, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("Reverse IPv6 ping failed: %s\n", result.Output)
			}
			return result != nil && result.Success
		}).WithTimeout(30*time.Second).WithPolling(3*time.Second).Should(BeTrue(), "Reverse IPv6 ping failed")
	})
})
