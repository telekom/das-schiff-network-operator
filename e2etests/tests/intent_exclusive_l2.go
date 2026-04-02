package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent-Exclusive L2 Connectivity test.
// Requires E2E_INTENT_MODE=true — intent reconciler produces NNCs, not legacy.
// Validates that the full pipeline (Intent CRDs → NNC → CRA-FRR → VLAN) works.
var _ = Describe("Intent-Exclusive: L2 Connectivity", Label("intent-exclusive", "l2"), func() {
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
		ns = "e2e-test-intent-excl-l2"

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())
		DeferCleanup(func() {
			cleanCtx := context.Background()
			_ = f.DeletePod(cleanCtx, ns, "intent-excl-l2-01")
			_ = f.DeletePod(cleanCtx, ns, "intent-excl-l2-02")
			_ = f.DeleteNamespace(cleanCtx, ns)
		})

		By("Applying L2 intent fixtures (L2A for VLAN 501)")
		l2aManifest, err := readTestdata("intent/l2/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, l2aManifest)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifest(context.Background(), l2aManifest)
		})

		By("Waiting for intent reconciler to apply NNC (VLAN 501 via CRA-FRR)")
		time.Sleep(10 * time.Second)
	})

	It("should create VLANs and allow L2 ping between pods", func() {
		cfg := f.Config

		By("Applying L2 NADs for macvlan pods (VLAN 501)")
		nadManifest, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nadManifest, ns)).To(Succeed())

		By("Creating intent-excl-l2-01 on worker-1 (VLAN 501)")
		Expect(f.CreateTestPod(ctx, ns, "intent-excl-l2-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		})).To(Succeed())

		By("Creating intent-excl-l2-02 on worker-2 (VLAN 501)")
		Expect(f.CreateTestPod(ctx, ns, "intent-excl-l2-02", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan02IPv4, cfg.Macvlan02IPv6),
		})).To(Succeed())

		By("Waiting for pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "intent-excl-l2-01", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "intent-excl-l2-02", cfg.PodReadyTimeout)).To(Succeed())

		By("Verifying L2 connectivity via IPv4 (cross-node)")
		Eventually(func() bool {
			r, _ := f.PingFromPod(ctx, ns, "intent-excl-l2-01", cfg.Macvlan02IPv4, 3)
			return r != nil && r.Success
		}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"Intent-exclusive: L2 IPv4 ping should work via intent pipeline")

		By("Verifying L2 connectivity via IPv6 (cross-node)")
		Eventually(func() bool {
			r, _ := f.PingFromPod(ctx, ns, "intent-excl-l2-01", cfg.Macvlan02IPv6, 3)
			return r != nil && r.Success
		}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"Intent-exclusive: L2 IPv6 ping should work via intent pipeline")
	})
})
