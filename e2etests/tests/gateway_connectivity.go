package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-04 & TC-05: VRF Gateway Connectivity.
var _ = Describe("Gateway Connectivity", Label("gateway", "smoke"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-gw"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

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

	Context("m2m gateway (TC-04)", func() {
		It("should allow bidirectional connectivity with m2mgw", func() {
			cfg := f.Config

			By("Creating macvlan-01 on worker-1 (VLAN 501, m2m)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-01", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
					cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
			})).To(Succeed())

			Expect(f.WaitForPodReady(ctx, ns, "macvlan-01", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying macvlan-01 can ping m2mgw (IPv4)")
			result, err := f.PingFromPod(ctx, ns, "macvlan-01", cfg.M2MGWIPv4, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(), "Ping to m2mgw IPv4 failed: %s", result.Output)

			By("Verifying macvlan-01 can ping m2mgw (IPv6)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "macvlan-01", cfg.M2MGWIPv6, 3)
				if r != nil && !r.Success {
					GinkgoWriter.Printf("IPv6 ping to m2mgw failed: %s\n", r.Output)
					neighOut, _, _ := f.ExecInPod(ctx, ns, "macvlan-01", "", []string{"ip", "-6", "neigh", "show"})
					addrOut, _, _ := f.ExecInPod(ctx, ns, "macvlan-01", "", []string{"ip", "-6", "addr", "show"})
					GinkgoWriter.Printf("macvlan-01 IPv6 neigh:\n%s\n", neighOut)
					GinkgoWriter.Printf("macvlan-01 IPv6 addr:\n%s\n", addrOut)
				}
				return r != nil && r.Success
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "Ping to m2mgw IPv6 failed")

			By("Verifying m2mgw can ping macvlan-01 (IPv4)")
			result, err = f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway", cfg.Macvlan01IPv4, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(), "Reverse ping from m2mgw failed: %s", result.Output)

			By("Verifying m2mgw can ping macvlan-01 (IPv6)")
			result, err = f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway", cfg.Macvlan01IPv6, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(), "Reverse ping from m2mgw IPv6 failed: %s", result.Output)
		})
	})

	Context("c2m gateway (TC-05)", func() {
		It("should allow bidirectional connectivity with c2mgw", func() {
			cfg := f.Config

			By("Creating macvlan-04 on worker-2 (VLAN 503, c2m)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-04", cfg.WorkerNode2, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan503", "ips": ["%s/24", "%s/64"]}]`,
					cfg.Macvlan04IPv4, cfg.Macvlan04IPv6),
			})).To(Succeed())

			Expect(f.WaitForPodReady(ctx, ns, "macvlan-04", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying macvlan-04 can ping c2mgw (IPv4)")
			result, err := f.PingFromPod(ctx, ns, "macvlan-04", cfg.C2MGWIPv4, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(), "Ping to c2mgw IPv4 failed: %s", result.Output)

			By("Verifying macvlan-04 can ping c2mgw (IPv6)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "macvlan-04", cfg.C2MGWIPv6, 3)
				if r != nil && !r.Success {
					GinkgoWriter.Printf("IPv6 ping to c2mgw failed: %s\n", r.Output)
					neighOut, _, _ := f.ExecInPod(ctx, ns, "macvlan-04", "", []string{"ip", "-6", "neigh", "show"})
					addrOut, _, _ := f.ExecInPod(ctx, ns, "macvlan-04", "", []string{"ip", "-6", "addr", "show"})
					GinkgoWriter.Printf("macvlan-04 IPv6 neigh:\n%s\n", neighOut)
					GinkgoWriter.Printf("macvlan-04 IPv6 addr:\n%s\n", addrOut)
				}
				return r != nil && r.Success
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "Ping to c2mgw IPv6 failed")

			By("Verifying c2mgw can ping macvlan-04 (IPv4)")
			result, err = f.PingFromCluster2Pod(ctx, "e2e-gateways", "c2m-gateway", cfg.Macvlan04IPv4, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(), "Reverse ping from c2mgw failed: %s", result.Output)

			By("Verifying c2mgw can ping macvlan-04 (IPv6)")
			result, err = f.PingFromCluster2Pod(ctx, "e2e-gateways", "c2m-gateway", cfg.Macvlan04IPv6, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(), "Reverse ping from c2mgw IPv6 failed: %s", result.Output)
		})
	})

	Context("cross-VRF isolation", func() {
		It("m2mgw should NOT reach c2m pod and c2mgw should NOT reach m2m pod", func() {
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

			Expect(f.WaitForPodReady(ctx, ns, "macvlan-01", cfg.PodReadyTimeout)).To(Succeed())
			Expect(f.WaitForPodReady(ctx, ns, "macvlan-04", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying m2mgw CANNOT reach c2m pod macvlan-04 (IPv4)")
			result, _ := f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway", cfg.Macvlan04IPv4, 3)
			Expect(result == nil || !result.Success).To(BeTrue(), "m2mgw should NOT reach c2m pod IPv4")

			By("Verifying m2mgw CANNOT reach c2m pod macvlan-04 (IPv6)")
			result, _ = f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway", cfg.Macvlan04IPv6, 3)
			Expect(result == nil || !result.Success).To(BeTrue(), "m2mgw should NOT reach c2m pod IPv6")

			By("Verifying c2mgw CANNOT reach m2m pod macvlan-01 (IPv4)")
			result, _ = f.PingFromCluster2Pod(ctx, "e2e-gateways", "c2m-gateway", cfg.Macvlan01IPv4, 3)
			Expect(result == nil || !result.Success).To(BeTrue(), "c2mgw should NOT reach m2m pod IPv4")

			By("Verifying c2mgw CANNOT reach m2m pod macvlan-01 (IPv6)")
			result, _ = f.PingFromCluster2Pod(ctx, "e2e-gateways", "c2m-gateway", cfg.Macvlan01IPv6, 3)
			Expect(result == nil || !result.Success).To(BeTrue(), "c2mgw should NOT reach m2m pod IPv6")
		})
	})
})
