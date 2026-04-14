package tests

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/telekom/das-schiff-network-operator/e2etests/config"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-I-MIRROR: Intent Traffic Mirror E2E tests.
// Validates that MirrorACLs in the NNC produce working GRE tunnels and tc mirror rules
// by sending traffic and verifying mirrored packets arrive at a collector pod.
// Tests each direction (ingress/egress) independently for both L2A and VRF sources.
var _ = Describe("Intent: Traffic Mirror", Label("intent", "mirror"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-intent-mirror"
	)

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

	// trafficMirrorYAML generates a TrafficMirror manifest for the given source and direction.
	trafficMirrorYAML := func(name, sourceKind, sourceName, direction string) []byte {
		return []byte(fmt.Sprintf(`apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: TrafficMirror
metadata:
  name: %s
  namespace: default
spec:
  source:
    kind: %s
    name: %q
  collector: "col-mirror"
  direction: %s
`, name, sourceKind, sourceName, direction))
	}

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

		By("Applying mirror infrastructure (mirror VRF, Collector, Inbound — no TrafficMirrors)")
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

	// createTrafficPods creates the mirror-capture, traffic-src, and traffic-dst pods
	// and waits for them to be ready.
	createTrafficPods := func(cfg *config.Config) {
		By("Creating mirror-capture pod on worker-1 (VLAN 590 / mirror VRF)")
		Expect(f.CreateTestPod(ctx, ns, "mirror-capture", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": `[{"name": "macvlan-vlan590", "ips": ["10.250.90.100/24"]}]`,
		}, withNetToolImage)).To(Succeed())

		By("Creating traffic-src pod on worker-1 (VLAN 501)")
		Expect(f.CreateTestPod(ctx, ns, "traffic-src", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`, cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Creating traffic-dst pod on worker-2 (VLAN 501)")
		Expect(f.CreateTestPod(ctx, ns, "traffic-dst", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`, cfg.Macvlan02IPv4, cfg.Macvlan02IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Waiting for all pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "mirror-capture", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "traffic-src", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "traffic-dst", cfg.PodReadyTimeout)).To(Succeed())

		By("Disabling IPv6 DAD and re-adding addresses on traffic pods")
		Expect(f.EnsureIPv6NoDad(ctx, ns, "traffic-src", cfg.Macvlan01IPv6, "net1")).To(Succeed())
		Expect(f.EnsureIPv6NoDad(ctx, ns, "traffic-dst", cfg.Macvlan02IPv6, "net1")).To(Succeed())
	}

	// verifyMirrorCapture sends pings and verifies GRE packets are captured at the collector.
	verifyMirrorCapture := func(srcIP, label string) {
		By(fmt.Sprintf("Sending ICMP traffic from traffic-src → traffic-dst (%s)", label))
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "traffic-src", srcIP, 5)
			return result != nil && result.Success
		}).WithTimeout(60*time.Second).WithPolling(3*time.Second).Should(BeTrue(),
			"Ping should succeed before checking mirror")

		By(fmt.Sprintf("Verifying mirrored GRE packets at collector (%s)", label))
		captureCmd := []string{
			"sh", "-c",
			"timeout 30 tcpdump -i net1 -c 5 proto gre 2>/dev/null | wc -l",
		}
		Eventually(func() bool {
			stdout, stderr, err := f.ExecInPod(ctx, ns, "mirror-capture", "tester", captureCmd)
			if err != nil {
				GinkgoWriter.Printf("tcpdump error: %v stderr: %s\n", err, stderr)
				return false
			}
			count := strings.TrimSpace(stdout)
			GinkgoWriter.Printf("[%s] GRE packet count: %s\n", label, count)
			return count != "" && count != "0"
		}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			fmt.Sprintf("Mirror-capture should receive GRE packets (%s)", label))
	}

	Context("L2A Direction: ingress-only", func() {
		It("should capture GRE packets with ingress-only mirror on br.501", func() {
			cfg := f.Config

			By("Applying ingress-only TrafficMirror for L2A")
			tm := trafficMirrorYAML("tm-l2-ingress", "Layer2Attachment", "l2a-base-vlan501", "ingress")
			Expect(f.ApplyManifest(ctx, tm)).To(Succeed())
			DeferCleanup(func() {
				_ = f.DeleteManifest(context.Background(), tm)
			})

			By("Waiting for NNC convergence")
			time.Sleep(15 * time.Second)

			createTrafficPods(cfg)
			verifyMirrorCapture(cfg.Macvlan02IPv4, "L2A ingress-only")
		})
	})

	Context("L2A Direction: egress-only", func() {
		It("should capture GRE packets with egress-only mirror on br.501", func() {
			cfg := f.Config

			By("Applying egress-only TrafficMirror for L2A")
			tm := trafficMirrorYAML("tm-l2-egress", "Layer2Attachment", "l2a-base-vlan501", "egress")
			Expect(f.ApplyManifest(ctx, tm)).To(Succeed())
			DeferCleanup(func() {
				_ = f.DeleteManifest(context.Background(), tm)
			})

			By("Waiting for NNC convergence")
			time.Sleep(15 * time.Second)

			createTrafficPods(cfg)
			verifyMirrorCapture(cfg.Macvlan02IPv4, "L2A egress-only")
		})
	})

	Context("VRF Direction: ingress-only (Inbound → m2m)", func() {
		It("should capture GRE packets with ingress-only mirror on m2m VRF", func() {
			cfg := f.Config

			By("Applying ingress-only TrafficMirror for VRF (Inbound)")
			tm := trafficMirrorYAML("tm-vrf-ingress", "Inbound", "ib-m2m-mirror", "ingress")
			Expect(f.ApplyManifest(ctx, tm)).To(Succeed())
			DeferCleanup(func() {
				_ = f.DeleteManifest(context.Background(), tm)
			})

			By("Waiting for NNC convergence")
			time.Sleep(15 * time.Second)

			createTrafficPods(cfg)
			verifyMirrorCapture(cfg.Macvlan02IPv4, "VRF ingress-only")
		})
	})

	Context("VRF Direction: egress-only (Inbound → m2m)", func() {
		It("should capture GRE packets with egress-only mirror on m2m VRF", func() {
			cfg := f.Config

			By("Applying egress-only TrafficMirror for VRF (Inbound)")
			tm := trafficMirrorYAML("tm-vrf-egress", "Inbound", "ib-m2m-mirror", "egress")
			Expect(f.ApplyManifest(ctx, tm)).To(Succeed())
			DeferCleanup(func() {
				_ = f.DeleteManifest(context.Background(), tm)
			})

			By("Waiting for NNC convergence")
			time.Sleep(15 * time.Second)

			createTrafficPods(cfg)
			verifyMirrorCapture(cfg.Macvlan02IPv4, "VRF egress-only")
		})
	})
})
