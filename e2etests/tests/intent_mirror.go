package tests

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
	corev1 "k8s.io/api/core/v1"
)

// TC-I-MIRROR: Intent Traffic Mirror E2E tests.
// Validates that MirrorACLs in the NNC produce working GRE tunnels and tc mirror rules
// by sending traffic and verifying mirrored packets arrive at a collector pod.
var _ = Describe("Intent: Traffic Mirror", Label("intent", "mirror"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-intent-mirror"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying intent base configs (VRFs, Networks, L2As)")
		base, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, base)).To(Succeed())

		By("Applying mirror infrastructure (mirror VRF, Collector, TrafficMirror)")
		mirror, err := readTestdata("intent/mirror/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, mirror)).To(Succeed())

		By("Applying mirror NAD for VLAN 590")
		nad, err := readTestdata("intent/mirror/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())

		By("Applying L2 NAD for VLAN 501 (traffic source)")
		l2Nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, l2Nad, ns)).To(Succeed())

		By("Waiting for NNC to converge with MirrorACLs")
		time.Sleep(15 * time.Second)
	})

	AfterEach(func() {
		By("Cleaning up test pods")
		_ = f.DeletePod(ctx, ns, "mirror-capture")
		_ = f.DeletePod(ctx, ns, "traffic-src")
		_ = f.DeletePod(ctx, ns, "traffic-dst")

		By("Deleting mirror manifests")
		mirror, _ := readTestdata("intent/mirror/manifests.yaml")
		_ = f.DeleteManifest(ctx, mirror)
	})

	// withNetToolImage overrides the container image with network-multitool (has tcpdump).
	withNetToolImage := func(spec *corev1.PodSpec) {
		spec.Containers[0].Image = "ghcr.io/srl-labs/network-multitool:v0.5.0"
		spec.Containers[0].Command = []string{"sleep", "86400"}
		spec.Containers[0].SecurityContext = &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"},
			},
		}
	}

	Context("L2 Mirror (VLAN 501 → mirror VRF)", func() {
		It("should capture mirrored ICMP packets at the collector", func() {
			cfg := f.Config

			By("Creating mirror-capture pod on worker-1 (VLAN 590 / mirror VRF)")
			Expect(f.CreateTestPod(ctx, ns, "mirror-capture", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": `[{"name": "macvlan-vlan590", "ips": ["10.250.90.100/24"]}]`,
			}, withNetToolImage)).To(Succeed())

			By("Creating traffic-src pod on worker-1 (VLAN 501)")
			Expect(f.CreateTestPod(ctx, ns, "traffic-src", cfg.WorkerNode1, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`, cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
			})).To(Succeed())

			By("Creating traffic-dst pod on worker-2 (VLAN 501)")
			Expect(f.CreateTestPod(ctx, ns, "traffic-dst", cfg.WorkerNode2, map[string]string{
				"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
					`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`, cfg.Macvlan02IPv4, cfg.Macvlan02IPv6),
			})).To(Succeed())

			By("Waiting for all pods to be ready")
			Expect(f.WaitForPodReady(ctx, ns, "mirror-capture", cfg.PodReadyTimeout)).To(Succeed())
			Expect(f.WaitForPodReady(ctx, ns, "traffic-src", cfg.PodReadyTimeout)).To(Succeed())
			Expect(f.WaitForPodReady(ctx, ns, "traffic-dst", cfg.PodReadyTimeout)).To(Succeed())

			By("Starting tcpdump on mirror-capture pod (background GRE capture)")
			// tcpdump captures GRE packets for 30s, writes count to stdout
			captureCmd := []string{
				"sh", "-c",
				"timeout 30 tcpdump -i net1 -c 5 proto gre 2>/dev/null | wc -l",
			}

			By("Sending ICMP traffic from traffic-src → traffic-dst")
			// Send a burst of pings — some will be mirrored
			Eventually(func() bool {
				result, _ := f.PingFromPod(ctx, ns, "traffic-src", cfg.Macvlan02IPv4, 5)
				return result != nil && result.Success
			}).WithTimeout(60 * time.Second).WithPolling(3 * time.Second).Should(BeTrue(),
				"L2 ping should work before checking mirror")

			By("Verifying mirrored GRE packets arrived at mirror-capture")
			Eventually(func() bool {
				stdout, stderr, err := f.ExecInPod(ctx, ns, "mirror-capture", "tester", captureCmd)
				if err != nil {
					GinkgoWriter.Printf("tcpdump error: %v stderr: %s\n", err, stderr)
					return false
				}
				count := strings.TrimSpace(stdout)
				GinkgoWriter.Printf("GRE packet count: %s\n", count)
				// We expect at least 1 GRE-encapsulated packet
				return count != "" && count != "0"
			}).WithTimeout(60 * time.Second).WithPolling(5 * time.Second).Should(BeTrue(),
				"Mirror-capture pod should receive GRE-encapsulated ICMP packets")
		})
	})
})
