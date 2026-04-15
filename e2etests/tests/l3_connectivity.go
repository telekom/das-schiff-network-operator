package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-02: L3 Connectivity (cross-VLAN, same VRF).
var _ = Describe("L3 Connectivity", Label("l3", "smoke"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-l3"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying L2 NADs for VLAN 501 and 502")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "macvlan-01")
		_ = f.DeletePod(ctx, ns, "macvlan-03")
	})

	It("should allow ping between pods on different VLANs in the same VRF", func() {
		cfg := f.Config

		By("Creating macvlan-01 on worker-1 (VLAN 501, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "macvlan-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Creating macvlan-03 on worker-2 (VLAN 502, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "macvlan-03", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan502", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan03IPv4, cfg.Macvlan03IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Waiting for pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "macvlan-01", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "macvlan-03", cfg.PodReadyTimeout)).To(Succeed())

		By("Verifying IPv4 cross-VLAN connectivity: macvlan-01 (501) → macvlan-03 (502)")
		result, err := f.PingFromPod(ctx, ns, "macvlan-01", cfg.Macvlan03IPv4, 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Success).To(BeTrue(), "Cross-VLAN IPv4 ping failed: %s", result.Output)

		By("Verifying IPv6 cross-VLAN connectivity: macvlan-01 (501) → macvlan-03 (502)")
		Eventually(func() bool {
			r, _ := f.PingFromPod(ctx, ns, "macvlan-01", cfg.Macvlan03IPv6, 3)
			return r != nil && r.Success
		}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "Cross-VLAN IPv6 ping failed")
	})
})
