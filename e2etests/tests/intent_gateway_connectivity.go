package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent-based Gateway Connectivity (Tier 2).
// Validates that intent CRDs can coexist with legacy config and
// existing VRF gateway connectivity remains functional.
var _ = Describe("Intent Gateway Connectivity", Label("intent", "gateway"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
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
	})

	AfterEach(func() {
		gwManifest, _ := readTestdata("intent/gateway/manifests.yaml")
		_ = f.DeleteManifest(ctx, gwManifest)
		l2aManifest, _ := readTestdata("intent/l2/manifests.yaml")
		_ = f.DeleteManifest(ctx, l2aManifest)
	})

	Context("m2m VRF gateway via Inbound CRD", func() {
		It("should maintain DC gateway connectivity after intent CRDs applied", func() {
			cfg := f.Config

			By("Waiting for intent CRDs to settle")
			time.Sleep(5 * time.Second)

			By("Verifying m2mgw can still ping cluster via m2m VRF (IPv4)")
			Eventually(func() bool {
				// Ping the IRB gateway address on VLAN 501 (10.250.0.1).
				// This verifies routes from DCGW to the m2m VRF are intact.
				r, err := f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway",
					"10.250.0.1", 3)
				return err == nil && r != nil && r.Success
			}).WithTimeout(cfg.BGPTimeout).WithPolling(5 * time.Second).Should(BeTrue(),
				"DC gateway connectivity broken after applying intent CRDs")
		})
	})
})
