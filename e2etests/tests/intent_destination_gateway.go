package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent Destination Gateway Connectivity — comprehensive gateway validation.
// Tests bidirectional connectivity to both m2m and c2m gateways, plus cross-VRF
// isolation from the gateway perspective. Uses cluster-2 gateway pods.
var _ = Describe("Intent: Destination Gateway Validation", Label("intent", "destination", "gateway"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-intent-dest-gw"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying intent base configs (VRFs, Networks, Destinations)")
		base, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, base)).To(Succeed())

		By("Applying L2A + Inbound for both VRFs")
		manifest, err := readTestdata("intent/destination-gw/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

		By("Applying NADs for VLAN 501 and 503")
		nad501, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad501, ns)).To(Succeed())

		nad503, err := readTestdata("l2-connectivity/nad-c2m.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad503, ns)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "dest-gw-m2m")
		_ = f.DeletePod(ctx, ns, "dest-gw-c2m")
		manifest, _ := readTestdata("intent/destination-gw/manifests.yaml")
		_ = f.DeleteManifest(ctx, manifest)
	})

	Context("m2m VRF bidirectional gateway (via dest-dcgw)", func() {
		It("should allow bidirectional connectivity between pod and m2m-gateway", func() {
			cfg := f.Config

			By("Creating dest-gw-m2m on worker-1 (VLAN 501, m2m)")
			Expect(f.CreateTestPod(ctx, ns, "dest-gw-m2m", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
					cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
			})).To(Succeed())

			Expect(f.WaitForPodReady(ctx, ns, "dest-gw-m2m", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying pod → m2m-gateway (IPv4)")
			result, err := f.PingFromPod(ctx, ns, "dest-gw-m2m", cfg.M2MGWIPv4, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(), "Pod → m2mgw IPv4 failed: %s", result.Output)

			By("Verifying pod → m2m-gateway (IPv6)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "dest-gw-m2m", cfg.M2MGWIPv6, 3)
				return r != nil && r.Success
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"Pod → m2mgw IPv6 failed")

			By("Verifying m2m-gateway → pod (IPv4, reverse direction)")
			result, err = f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway",
				cfg.Macvlan01IPv4, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(),
				"m2mgw → pod IPv4 failed (destination prefixes may not produce correct static routes): %s", result.Output)

			By("Verifying m2m-gateway → pod (IPv6, reverse direction)")
			Eventually(func() bool {
				r, _ := f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway",
					cfg.Macvlan01IPv6, 3)
				return r != nil && r.Success
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"m2mgw → pod IPv6 failed")
		})
	})

	Context("c2m VRF bidirectional gateway (via dest-dcgw-c2m)", func() {
		It("should allow bidirectional connectivity between pod and c2m-gateway", func() {
			cfg := f.Config

			By("Creating dest-gw-c2m on worker-2 (VLAN 503, c2m)")
			Expect(f.CreateTestPod(ctx, ns, "dest-gw-c2m", cfg.WorkerNode2, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan503", "ips": ["%s/24", "%s/64"]}]`,
					cfg.Macvlan04IPv4, cfg.Macvlan04IPv6),
			})).To(Succeed())

			Expect(f.WaitForPodReady(ctx, ns, "dest-gw-c2m", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying pod → c2m-gateway (IPv4)")
			result, err := f.PingFromPod(ctx, ns, "dest-gw-c2m", cfg.C2MGWIPv4, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(), "Pod → c2mgw IPv4 failed: %s", result.Output)

			By("Verifying pod → c2m-gateway (IPv6)")
			Eventually(func() bool {
				r, _ := f.PingFromPod(ctx, ns, "dest-gw-c2m", cfg.C2MGWIPv6, 3)
				return r != nil && r.Success
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"Pod → c2mgw IPv6 failed")

			By("Verifying c2m-gateway → pod (IPv4, reverse direction)")
			result, err = f.PingFromCluster2Pod(ctx, "e2e-gateways", "c2m-gateway",
				cfg.Macvlan04IPv4, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Success).To(BeTrue(),
				"c2mgw → pod IPv4 failed (c2m destination prefixes not working): %s", result.Output)

			By("Verifying c2m-gateway → pod (IPv6, reverse direction)")
			Eventually(func() bool {
				r, _ := f.PingFromCluster2Pod(ctx, "e2e-gateways", "c2m-gateway",
					cfg.Macvlan04IPv6, 3)
				return r != nil && r.Success
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"c2mgw → pod IPv6 failed")
		})
	})

	Context("cross-VRF gateway isolation", func() {
		It("should prevent gateways from reaching pods in the other VRF", func() {
			cfg := f.Config

			By("Creating dest-gw-m2m on worker-1 (VLAN 501, m2m)")
			Expect(f.CreateTestPod(ctx, ns, "dest-gw-m2m", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
					cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
			})).To(Succeed())

			By("Creating dest-gw-c2m on worker-2 (VLAN 503, c2m)")
			Expect(f.CreateTestPod(ctx, ns, "dest-gw-c2m", cfg.WorkerNode2, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan503", "ips": ["%s/24", "%s/64"]}]`,
					cfg.Macvlan04IPv4, cfg.Macvlan04IPv6),
			})).To(Succeed())

			Expect(f.WaitForPodReady(ctx, ns, "dest-gw-m2m", cfg.PodReadyTimeout)).To(Succeed())
			Expect(f.WaitForPodReady(ctx, ns, "dest-gw-c2m", cfg.PodReadyTimeout)).To(Succeed())

			By("Verifying m2m-gateway CANNOT reach c2m pod (IPv4)")
			result, _ := f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway",
				cfg.Macvlan04IPv4, 3)
			Expect(result == nil || !result.Success).To(BeTrue(),
				"m2m-gateway should NOT reach c2m pod — VRF isolation broken")

			By("Verifying m2m-gateway CANNOT reach c2m pod (IPv6)")
			result, _ = f.PingFromCluster2Pod(ctx, "e2e-gateways", "m2m-gateway",
				cfg.Macvlan04IPv6, 3)
			Expect(result == nil || !result.Success).To(BeTrue(),
				"m2m-gateway should NOT reach c2m pod IPv6 — VRF isolation broken")

			By("Verifying c2m-gateway CANNOT reach m2m pod (IPv4)")
			result, _ = f.PingFromCluster2Pod(ctx, "e2e-gateways", "c2m-gateway",
				cfg.Macvlan01IPv4, 3)
			Expect(result == nil || !result.Success).To(BeTrue(),
				"c2m-gateway should NOT reach m2m pod — VRF isolation broken")

			By("Verifying c2m-gateway CANNOT reach m2m pod (IPv6)")
			result, _ = f.PingFromCluster2Pod(ctx, "e2e-gateways", "c2m-gateway",
				cfg.Macvlan01IPv6, 3)
			Expect(result == nil || !result.Success).To(BeTrue(),
				"c2m-gateway should NOT reach m2m pod IPv6 — VRF isolation broken")
		})
	})
})
