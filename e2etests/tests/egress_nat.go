package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-08: Egress NAT.
var _ = Describe("Egress NAT", Label("egress"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-egress"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())
	})

	Context("m2m egress (TC-08)", func() {
		BeforeEach(func() {
			By("Applying m2m egress manifests (Coil Egress + test pod)")
			manifest, err := readTestdata("egress/manifests.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifestInNamespace(ctx, manifest, ns)).To(Succeed())
		})

		AfterEach(func() {
			manifest, _ := readTestdata("egress/manifests.yaml")
			_ = f.DeleteManifest(ctx, manifest)
		})

		It("should NAT egress traffic to m2mgw through Coil egress", func() {
			cfg := f.Config

			By("Waiting for egress pod to be ready")
			Expect(f.WaitForPodReady(ctx, ns, "egress-via-nat-01", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying egress-via-nat-01 can reach m2mgw (IPv4)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "egress-via-nat-01", cfg.M2MGWIPv4, 3)
				return r != nil && r.Success
			}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "Egress pod cannot reach m2mgw IPv4")

			By("Verifying egress-via-nat-01 can reach m2mgw (IPv6)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "egress-via-nat-01", cfg.M2MGWIPv6, 3)
				return r != nil && r.Success
			}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "Egress pod cannot reach m2mgw IPv6")

			By("Verifying egress-via-nat-01 CANNOT reach c2mgw (wrong VRF, IPv4)")
			Expect(f.AssertNoConnectivity(ctx, ns, "egress-via-nat-01", cfg.C2MGWIPv4)).To(Succeed())

			By("Verifying egress-via-nat-01 CANNOT reach c2mgw (wrong VRF, IPv6)")
			Expect(f.AssertNoConnectivity(ctx, ns, "egress-via-nat-01", cfg.C2MGWIPv6)).To(Succeed())
		})
	})

	Context("c2m egress (TC-08b)", func() {
		BeforeEach(func() {
			By("Applying c2m egress manifests (Coil Egress + test pod)")
			manifest, err := readTestdata("egress/manifests-c2m.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifestInNamespace(ctx, manifest, ns)).To(Succeed())
		})

		AfterEach(func() {
			manifest, _ := readTestdata("egress/manifests-c2m.yaml")
			_ = f.DeleteManifest(ctx, manifest)
		})

		It("should NAT egress traffic to c2mgw through Coil egress", func() {
			cfg := f.Config

			By("Waiting for c2m egress pod to be ready")
			Expect(f.WaitForPodReady(ctx, ns, "egress-via-nat-c2m-01", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying egress-via-nat-c2m-01 can reach c2mgw (IPv4)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "egress-via-nat-c2m-01", cfg.C2MGWIPv4, 3)
				return r != nil && r.Success
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"c2m egress pod cannot reach c2mgw IPv4")

			By("Verifying egress-via-nat-c2m-01 can reach c2mgw (IPv6)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "egress-via-nat-c2m-01", cfg.C2MGWIPv6, 3)
				return r != nil && r.Success
			}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "c2m egress pod cannot reach c2mgw IPv6")

			By("Verifying egress-via-nat-c2m-01 CANNOT reach m2mgw (wrong VRF, IPv4)")
			Expect(f.AssertNoConnectivity(ctx, ns, "egress-via-nat-c2m-01", cfg.M2MGWIPv4)).To(Succeed())

			By("Verifying egress-via-nat-c2m-01 CANNOT reach m2mgw (wrong VRF, IPv6)")
			Expect(f.AssertNoConnectivity(ctx, ns, "egress-via-nat-c2m-01", cfg.M2MGWIPv6)).To(Succeed())
		})
	})
})
