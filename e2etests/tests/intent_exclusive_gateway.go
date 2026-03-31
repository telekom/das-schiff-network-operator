package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent-Exclusive Gateway Connectivity.
// Requires E2E_INTENT_MODE=true — intent reconciler produces NNCs, not legacy.
// Validates that the Inbound CRD correctly creates routes allowing the DC gateway
// to reach the m2m VRF IRB address via EVPN.
var _ = Describe("Intent-Exclusive: Gateway Connectivity", Label("intent-exclusive", "gateway"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		Expect(f.IsIntentMode()).To(BeTrue(), "intent-exclusive tests require E2E_INTENT_MODE=true")
		ctx = context.Background()

		By("Applying intent base configs (VRFs, Networks, Destinations)")
		baseCfg, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, baseCfg)).To(Succeed())

		By("Applying intent gateway Inbound manifest")
		gwManifest, err := readTestdata("intent/gateway/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, gwManifest)).To(Succeed())

		By("Applying L2A for VLAN 501 (intent CRD for m2m VRF)")
		l2aManifest, err := readTestdata("intent/l2/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, l2aManifest)).To(Succeed())

		By("Waiting for intent reconciler to process and CRA-FRR to converge")
		time.Sleep(15 * time.Second)
	})

	AfterEach(func() {
		gwManifest, _ := readTestdata("intent/gateway/manifests.yaml")
		_ = f.DeleteManifest(ctx, gwManifest)
		l2aManifest, _ := readTestdata("intent/l2/manifests.yaml")
		_ = f.DeleteManifest(ctx, l2aManifest)
	})

	Context("m2m VRF gateway via Inbound CRD", func() {
		It("should allow DC gateway to reach m2m VRF IRB address", func() {
			cfg := f.Config

			By("Verifying m2mgw can ping m2m VRF IRB (10.250.0.1) via intent-produced NNC")
			Eventually(func() bool {
				r, err := f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway",
					"10.250.0.1", 3)
				return err == nil && r != nil && r.Success
			}).WithTimeout(cfg.BGPTimeout).WithPolling(5 * time.Second).Should(BeTrue(),
				"Intent-exclusive: DC gateway should reach m2m VRF via intent pipeline")
		})
	})
})
