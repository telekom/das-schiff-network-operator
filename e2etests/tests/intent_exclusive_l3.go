package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent-Exclusive L3 Connectivity (cross-VLAN, same VRF).
// Requires E2E_INTENT_MODE=true — intent reconciler produces NNCs, not legacy.
// Validates that IRB routing between VLANs 501 and 502 works when driven solely
// by the intent pipeline.
var _ = Describe("Intent-Exclusive: L3 Connectivity", Label("intent-exclusive", "l3"), func() {
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
		ns = "e2e-test-intent-excl-l3"

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())
		DeferCleanup(func() {
			cleanCtx := context.Background()
			_ = f.DeletePod(cleanCtx, ns, "intent-excl-l3-01")
			_ = f.DeletePod(cleanCtx, ns, "intent-excl-l3-03")
			_ = f.DeleteNamespace(cleanCtx, ns)
		})

		By("Applying intent base configs")
		base, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, base)).To(Succeed())

		By("Applying L3 intent fixtures (VLAN 501 + 502, same VRF)")
		l3, err := readTestdata("intent/l3/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, l3)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifest(context.Background(), l3)
		})

		By("Waiting for NNC to contain VRF m2m on both worker nodes")
		Expect(f.WaitForNNCVRFs(ctx, f.Config.WorkerNode1, []string{"m2m"}, 60*time.Second)).To(Succeed())
		Expect(f.WaitForNNCVRFs(ctx, f.Config.WorkerNode2, []string{"m2m"}, 60*time.Second)).To(Succeed())
	})

	It("should route between VLANs 501 and 502 in the same VRF via intent pipeline", func() {
		cfg := f.Config

		By("Applying L2 NADs for VLAN 501 and 502")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())

		By("Creating intent-excl-l3-01 on worker-1 (VLAN 501, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "intent-excl-l3-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Creating intent-excl-l3-03 on worker-2 (VLAN 502, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "intent-excl-l3-03", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan502", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan03IPv4, cfg.Macvlan03IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Waiting for pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "intent-excl-l3-01", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "intent-excl-l3-03", cfg.PodReadyTimeout)).To(Succeed())

		By("Disabling IPv6 DAD and re-adding addresses")
		Expect(f.EnsureIPv6NoDad(ctx, ns, "intent-excl-l3-01", cfg.Macvlan01IPv6, "net1")).To(Succeed())
		Expect(f.EnsureIPv6NoDad(ctx, ns, "intent-excl-l3-03", cfg.Macvlan03IPv6, "net1")).To(Succeed())

		By("Verifying IPv4 cross-VLAN connectivity: 501 → 502")
		Eventually(func() bool {
			r, _ := f.PingFromPod(ctx, ns, "intent-excl-l3-01", cfg.Macvlan03IPv4, 5)
			return r != nil && r.Success
		}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"Intent-exclusive: Cross-VLAN IPv4 ping should work via intent pipeline")

		By("Verifying IPv6 cross-VLAN connectivity: 501 → 502")
		Eventually(func() bool {
			r, _ := f.PingFromPod(ctx, ns, "intent-excl-l3-01", cfg.Macvlan03IPv6, 3)
			return r != nil && r.Success
		}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"Intent-exclusive: Cross-VLAN IPv6 ping should work via intent pipeline")
	})
})
