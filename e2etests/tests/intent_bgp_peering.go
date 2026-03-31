package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent-based BGP Peering test using BGPPeering, Layer2Attachment, and Inbound CRDs.
// Validates that intent CRDs can be applied alongside legacy config without conflict.
// Full pipeline validation is in Tier 1 envtest (pkg/reconciler/intent/reconciler_test.go).
var _ = Describe("Intent BGP Peering", Label("intent", "bgp"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Applying intent base configs (VRFs, Networks, Destinations)")
		baseManifest, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, baseManifest)).To(Succeed())
	})

	AfterEach(func() {
		manifest, _ := readTestdata("intent/bgp/manifests.yaml")
		_ = f.DeleteManifest(ctx, manifest)
	})

	It("should accept BGPPeering intent CRDs without breaking existing connectivity", func() {
		cfg := f.Config

		By("Applying intent BGP manifests (L2A + Inbound + BGPPeering)")
		manifest, err := readTestdata("intent/bgp/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

		By("Waiting for CRDs to settle")
		time.Sleep(5 * time.Second)

		By("Verifying existing m2m VRF BGP peering is still functional")
		// The legacy BGPaaS test runs separately; here we just verify
		// that applying intent CRDs doesn't break the FRR BGP setup.
		Eventually(func() bool {
			summary, err := f.GetBGPSummaryOnKindNodeVRF(ctx, cfg.WorkerNode1, cfg.VRFM2M)
			if err != nil {
				return false
			}
			// Just verify FRR is healthy and can return a summary.
			return summary != nil
		}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"FRR BGP should remain operational after applying intent CRDs")

		By("Verifying existing connectivity via m2m VRF is unaffected")
		Eventually(func() bool {
			r, err := f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway",
				"10.250.0.1", 3)
			return err == nil && r != nil && r.Success
		}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"m2mgw connectivity should remain after applying intent CRDs")
	})
})
