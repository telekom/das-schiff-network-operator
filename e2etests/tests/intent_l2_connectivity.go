package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-I01: Intent L2 Connectivity (same VLAN).
var _ = Describe("Intent: L2 Connectivity", Label("intent", "l2"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-intent-l2"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying intent base configs")
		base, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, base)).To(Succeed())

		By("Applying L2 intent fixtures")
		l2, err := readTestdata("intent/l2/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, l2)).To(Succeed())

		By("Applying L2 NADs for macvlan pods")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "intent-l2-01")
		_ = f.DeletePod(ctx, ns, "intent-l2-02")
	})

	It("should allow ping between pods on the same VLAN using intent CRDs", func() {
		cfg := f.Config

		By("Creating intent-l2-01 on worker-1 (VLAN 501)")
		Expect(f.CreateTestPod(ctx, ns, "intent-l2-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Creating intent-l2-02 on worker-2 (VLAN 501)")
		Expect(f.CreateTestPod(ctx, ns, "intent-l2-02", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan02IPv4, cfg.Macvlan02IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Waiting for pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "intent-l2-01", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "intent-l2-02", cfg.PodReadyTimeout)).To(Succeed())

		By("Disabling IPv6 DAD and re-adding addresses")
		Expect(f.EnsureIPv6NoDad(ctx, ns, "intent-l2-01", cfg.Macvlan01IPv6, "net1")).To(Succeed())
		Expect(f.EnsureIPv6NoDad(ctx, ns, "intent-l2-02", cfg.Macvlan02IPv6, "net1")).To(Succeed())

		By("Verifying L2 connectivity via IPv4")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "intent-l2-01", cfg.Macvlan02IPv4, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("IPv4 ping failed: %s\n", result.Output)
			}
			return result != nil && result.Success
		}).WithTimeout(60*time.Second).WithPolling(3*time.Second).Should(BeTrue(), "IPv4 ping failed")

		By("Verifying L2 connectivity via IPv6")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "intent-l2-01", cfg.Macvlan02IPv6, 3)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("IPv6 ping failed: %s\n", result.Output)
			}
			return result != nil && result.Success
		}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "IPv6 ping failed")
	})
})
