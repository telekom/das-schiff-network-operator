package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-09: Anycast Gateway.
var _ = Describe("Anycast Gateway", Label("l2", "intent"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-anycast"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())

		nadC2M, err := readTestdata("l2-connectivity/nad-c2m.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nadC2M, ns)).To(Succeed())
	})

	AfterEach(func() {
		for _, name := range []string{
			"macvlan-01", "macvlan-02",
			"macvlan-v502-01", "macvlan-v502-02",
			"macvlan-v503-01", "macvlan-v503-02",
		} {
			_ = f.DeletePod(ctx, ns, name)
		}
	})

	Context("VLAN 501 (m2m)", func() {
		It("should present the same anycast gateway MAC on both nodes", func() {
			cfg := f.Config

			By("Creating macvlan-01 on worker-1 (VLAN 501)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-01", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
					cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
			})).To(Succeed())

			By("Creating macvlan-02 on worker-2 (VLAN 501)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-02", cfg.WorkerNode2, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
					cfg.Macvlan02IPv4, cfg.Macvlan02IPv6),
			})).To(Succeed())

			By("Waiting for pods to be ready")
			Expect(f.WaitForPodReady(ctx, ns, "macvlan-01", cfg.PodReadyTimeout)).To(Succeed())
			Expect(f.WaitForPodReady(ctx, ns, "macvlan-02", cfg.PodReadyTimeout)).To(Succeed())

			gwIPv4 := "10.250.0.1"
			gwIPv6 := "fd94:685b:30cf:501::1"

			By("Resolving gateway MAC from macvlan-01 (IPv4)")
			mac1 := resolveGatewayMAC(ctx, f, ns, "macvlan-01", gwIPv4)

			By("Resolving gateway MAC from macvlan-02 (IPv4)")
			mac2 := resolveGatewayMAC(ctx, f, ns, "macvlan-02", gwIPv4)

			By(fmt.Sprintf("Verifying both see the same anycast MAC via ARP (expected: %s)", cfg.AnycastMAC))
			Expect(mac1).To(Equal(mac2), "Gateway MACs differ between nodes (IPv4)")
			Expect(mac1).To(Equal(cfg.AnycastMAC), "Gateway MAC does not match configured anycast MAC (IPv4)")

			By("Resolving gateway MAC from macvlan-01 (IPv6)")
			mac1v6 := resolveGatewayMAC(ctx, f, ns, "macvlan-01", gwIPv6)

			By("Resolving gateway MAC from macvlan-02 (IPv6)")
			mac2v6 := resolveGatewayMAC(ctx, f, ns, "macvlan-02", gwIPv6)

			By(fmt.Sprintf("Verifying both see the same anycast MAC via NDP (expected: %s)", cfg.AnycastMAC))
			Expect(mac1v6).To(Equal(mac2v6), "Gateway MACs differ between nodes (IPv6)")
			Expect(mac1v6).To(Equal(cfg.AnycastMAC), "Gateway MAC does not match configured anycast MAC (IPv6)")
		})
	})

	Context("VLAN 502 (m2m, cross-VLAN)", func() {
		It("should present the same anycast gateway MAC on both nodes", func() {
			cfg := f.Config

			By("Creating macvlan-v502-01 on worker-1 (VLAN 502)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-v502-01", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan502", "ips": ["%s/24", "%s/64"]}]`,
					cfg.AnycastV502Pod01IPv4, cfg.AnycastV502Pod01IPv6),
			})).To(Succeed())

			By("Creating macvlan-v502-02 on worker-2 (VLAN 502)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-v502-02", cfg.WorkerNode2, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan502", "ips": ["%s/24", "%s/64"]}]`,
					cfg.AnycastV502Pod02IPv4, cfg.AnycastV502Pod02IPv6),
			})).To(Succeed())

			By("Waiting for pods to be ready")
			Expect(f.WaitForPodReady(ctx, ns, "macvlan-v502-01", cfg.PodReadyTimeout)).To(Succeed())
			Expect(f.WaitForPodReady(ctx, ns, "macvlan-v502-02", cfg.PodReadyTimeout)).To(Succeed())

			gwIPv4 := "10.250.1.1"
			gwIPv6 := "fd94:685b:30cf:502::1"

			By("Resolving VLAN 502 gateway MAC from macvlan-v502-01 (IPv4)")
			mac1 := resolveGatewayMAC(ctx, f, ns, "macvlan-v502-01", gwIPv4)

			By("Resolving VLAN 502 gateway MAC from macvlan-v502-02 (IPv4)")
			mac2 := resolveGatewayMAC(ctx, f, ns, "macvlan-v502-02", gwIPv4)

			By(fmt.Sprintf("Verifying both see the same VLAN 502 anycast MAC (expected: %s)", cfg.AnycastMACVlan502))
			Expect(mac1).To(Equal(mac2), "VLAN 502 gateway MACs differ between nodes (IPv4)")
			Expect(mac1).To(Equal(cfg.AnycastMACVlan502), "VLAN 502 gateway MAC does not match configured anycast MAC (IPv4)")

			By("Resolving VLAN 502 gateway MAC from macvlan-v502-01 (IPv6)")
			mac1v6 := resolveGatewayMAC(ctx, f, ns, "macvlan-v502-01", gwIPv6)

			By("Resolving VLAN 502 gateway MAC from macvlan-v502-02 (IPv6)")
			mac2v6 := resolveGatewayMAC(ctx, f, ns, "macvlan-v502-02", gwIPv6)

			By(fmt.Sprintf("Verifying both see the same VLAN 502 anycast MAC via NDP (expected: %s)", cfg.AnycastMACVlan502))
			Expect(mac1v6).To(Equal(mac2v6), "VLAN 502 gateway MACs differ between nodes (IPv6)")
			Expect(mac1v6).To(Equal(cfg.AnycastMACVlan502), "VLAN 502 gateway MAC does not match configured anycast MAC (IPv6)")
		})
	})

	Context("VLAN 503 (c2m)", func() {
		It("should present the same anycast gateway MAC on both nodes", func() {
			cfg := f.Config

			By("Creating macvlan-v503-01 on worker-1 (VLAN 503)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-v503-01", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan503", "ips": ["%s/24", "%s/64"]}]`,
					cfg.AnycastV503Pod01IPv4, cfg.AnycastV503Pod01IPv6),
			})).To(Succeed())

			By("Creating macvlan-v503-02 on worker-2 (VLAN 503)")
			Expect(f.CreateTestPod(ctx, ns, "macvlan-v503-02", cfg.WorkerNode2, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan503", "ips": ["%s/24", "%s/64"]}]`,
					cfg.AnycastV503Pod02IPv4, cfg.AnycastV503Pod02IPv6),
			})).To(Succeed())

			By("Waiting for pods to be ready")
			Expect(f.WaitForPodReady(ctx, ns, "macvlan-v503-01", cfg.PodReadyTimeout)).To(Succeed())
			Expect(f.WaitForPodReady(ctx, ns, "macvlan-v503-02", cfg.PodReadyTimeout)).To(Succeed())

			gwIPv4 := "10.250.30.1"
			gwIPv6 := "fd94:685b:30cf:503::1"

			By("Resolving VLAN 503 gateway MAC from macvlan-v503-01 (IPv4)")
			mac1 := resolveGatewayMAC(ctx, f, ns, "macvlan-v503-01", gwIPv4)

			By("Resolving VLAN 503 gateway MAC from macvlan-v503-02 (IPv4)")
			mac2 := resolveGatewayMAC(ctx, f, ns, "macvlan-v503-02", gwIPv4)

			By(fmt.Sprintf("Verifying both see the same VLAN 503 anycast MAC (expected: %s)", cfg.AnycastMAC))
			Expect(mac1).To(Equal(mac2), "VLAN 503 gateway MACs differ between nodes (IPv4)")
			Expect(mac1).To(Equal(cfg.AnycastMAC), "VLAN 503 gateway MAC does not match configured anycast MAC (IPv4)")

			By("Resolving VLAN 503 gateway MAC from macvlan-v503-01 (IPv6)")
			mac1v6 := resolveGatewayMAC(ctx, f, ns, "macvlan-v503-01", gwIPv6)

			By("Resolving VLAN 503 gateway MAC from macvlan-v503-02 (IPv6)")
			mac2v6 := resolveGatewayMAC(ctx, f, ns, "macvlan-v503-02", gwIPv6)

			By(fmt.Sprintf("Verifying both see the same VLAN 503 anycast MAC via NDP (expected: %s)", cfg.AnycastMAC))
			Expect(mac1v6).To(Equal(mac2v6), "VLAN 503 gateway MACs differ between nodes (IPv6)")
			Expect(mac1v6).To(Equal(cfg.AnycastMAC), "VLAN 503 gateway MAC does not match configured anycast MAC (IPv6)")
		})
	})
})

// resolveGatewayMAC pings the gateway and reads its neighbour entry until the
// MAC is resolved. The first L2 spec may run before the CRA has finished
// programming the VLAN, so a single-shot lookup is racy.
func resolveGatewayMAC(ctx context.Context, f *framework.Framework, ns, pod, gwIP string) string {
	var mac string
	Eventually(func() error {
		if result, err := f.PingFromPod(ctx, ns, pod, gwIP, 1); err != nil || result == nil || !result.Success {
			GinkgoWriter.Printf("%s: ping %s not yet successful (err=%v)\n", pod, gwIP, err)
		}
		var err error
		mac, err = f.GetGatewayMAC(ctx, ns, pod, gwIP)
		return err
	}).WithTimeout(60*time.Second).WithPolling(3*time.Second).Should(Succeed(),
		"gateway %s not resolvable from %s", gwIP, pod)
	return mac
}
