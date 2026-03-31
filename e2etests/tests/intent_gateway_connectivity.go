package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent-based Gateway Connectivity (Tier 2).
var _ = Describe("Intent Gateway Connectivity", Label("intent", "gateway"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-intent-gw"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying intent base configs (VRFs, Networks, Destinations)")
		baseCfg, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, baseCfg)).To(Succeed())

		By("Applying intent gateway Inbound manifest")
		gwManifest, err := readTestdata("intent/gateway/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, gwManifest)).To(Succeed())

		By("Applying macvlan NAD for VLAN 501")
		nad501, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad501, ns)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "macvlan-intent-gw")

		gwManifest, _ := readTestdata("intent/gateway/manifests.yaml")
		_ = f.DeleteManifest(ctx, gwManifest)
	})

	Context("m2m VRF gateway via Inbound CRD", func() {
		It("should allow connectivity to the gateway VIP from a macvlan pod", func() {
			cfg := f.Config

			By("Creating macvlan-intent-gw on worker-1 (VLAN 501, m2m)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-intent-gw", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan501", "ips": ["%s/24"]}]`,
					cfg.Macvlan01IPv4),
			})).To(Succeed())

			Expect(f.WaitForPodReady(ctx, ns, "macvlan-intent-gw", cfg.PodReadyTimeout)).To(Succeed())

			By("Waiting for BGP route propagation")
			time.Sleep(10 * time.Second)

			By("Verifying macvlan-intent-gw can ping gateway VIP 10.250.4.10 (IPv4)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "macvlan-intent-gw", "10.250.4.10", 3)
				return r != nil && r.Success
			}).WithTimeout(cfg.BGPTimeout).WithPolling(5 * time.Second).Should(BeTrue(),
				"Ping to intent gateway VIP 10.250.4.10 failed")
		})
	})
})
