package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-I02: Intent L3 Connectivity (cross-VLAN, same VRF).
var _ = Describe("Intent: L3 Connectivity", Label("intent", "l3"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-intent-l3"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying intent base configs")
		base, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, base)).To(Succeed())

		By("Applying L3 intent fixtures (VLAN 501 + 502, same VRF)")
		l3, err := readTestdata("intent/l3/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, l3)).To(Succeed())

		By("Applying L2 NADs for VLAN 501 and 502")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "intent-l3-01")
		_ = f.DeletePod(ctx, ns, "intent-l3-03")
	})

	It("should allow ping between pods on different VLANs in the same VRF using intent CRDs", func() {
		cfg := f.Config

		By("Creating intent-l3-01 on worker-1 (VLAN 501, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "intent-l3-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Creating intent-l3-03 on worker-2 (VLAN 502, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "intent-l3-03", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan502", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan03IPv4, cfg.Macvlan03IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Waiting for pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "intent-l3-01", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "intent-l3-03", cfg.PodReadyTimeout)).To(Succeed())

		By("Disabling IPv6 DAD and re-adding addresses")
		Expect(f.EnsureIPv6NoDad(ctx, ns, "intent-l3-01", cfg.Macvlan01IPv6, "net1")).To(Succeed())
		Expect(f.EnsureIPv6NoDad(ctx, ns, "intent-l3-03", cfg.Macvlan03IPv6, "net1")).To(Succeed())

		By("Verifying IPv4 cross-VLAN connectivity: intent-l3-01 (501) → intent-l3-03 (502)")
		result, err := f.PingFromPod(ctx, ns, "intent-l3-01", cfg.Macvlan03IPv4, 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Success).To(BeTrue(), "Cross-VLAN IPv4 ping failed: %s", result.Output)

		By("Verifying IPv6 cross-VLAN connectivity: intent-l3-01 (501) → intent-l3-03 (502)")
		Eventually(func() bool {
			r, _ := f.PingFromPod(ctx, ns, "intent-l3-01", cfg.Macvlan03IPv6, 3)
			return r != nil && r.Success
		}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "Cross-VLAN IPv6 ping failed")
	})
})
