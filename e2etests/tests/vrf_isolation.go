package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-03: VRF Isolation.
var _ = Describe("VRF Isolation", Label("vrf"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-vrf"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying NADs for m2m (VLAN 501) and c2m (VLAN 503)")
		nad501, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad501, ns)).To(Succeed())

		nad503, err := readTestdata("l2-connectivity/nad-c2m.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad503, ns)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "macvlan-01")
		_ = f.DeletePod(ctx, ns, "macvlan-04")
	})

	It("should block connectivity between different VRFs", func() {
		cfg := f.Config

		By("Creating macvlan-01 on worker-1 (VLAN 501, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "macvlan-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		})).To(Succeed())

		By("Creating macvlan-04 on worker-2 (VLAN 503, c2m)")
		Expect(f.CreateTestPod(ctx, ns, "macvlan-04", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan503", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan04IPv4, cfg.Macvlan04IPv6),
		})).To(Succeed())

		By("Waiting for pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "macvlan-01", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "macvlan-04", cfg.PodReadyTimeout)).To(Succeed())

		By("Verifying macvlan-01 (m2m) CANNOT ping macvlan-04 (c2m) IPv4")
		Expect(f.AssertNoConnectivity(ctx, ns, "macvlan-01", cfg.Macvlan04IPv4)).To(Succeed())

		By("Verifying macvlan-04 (c2m) CANNOT ping macvlan-01 (m2m) IPv4")
		Expect(f.AssertNoConnectivity(ctx, ns, "macvlan-04", cfg.Macvlan01IPv4)).To(Succeed())

		By("Verifying macvlan-01 (m2m) CANNOT ping macvlan-04 (c2m) IPv6")
		Expect(f.AssertNoConnectivity(ctx, ns, "macvlan-01", cfg.Macvlan04IPv6)).To(Succeed())

		By("Verifying macvlan-04 (c2m) CANNOT ping macvlan-01 (m2m) IPv6")
		Expect(f.AssertNoConnectivity(ctx, ns, "macvlan-04", cfg.Macvlan01IPv6)).To(Succeed())
	})
})
