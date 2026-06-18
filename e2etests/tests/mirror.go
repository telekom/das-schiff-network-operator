package tests

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/config"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
	corev1 "k8s.io/api/core/v1"
)

// TC-MIRROR: Traffic Mirror E2E tests.
// Validates that MirrorSelector/MirrorTarget CRDs produce working GRE tunnels and
// tc mirror rules by sending traffic and verifying GRE-encapsulated packets arrive
// at a collector pod. Each direction (ingress/egress) is tested for both a Layer2
// and a VRF source.
var _ = Describe("Traffic Mirror", Label("mirror"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-mirror"
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

	// mirrorSelectorYAML generates a MirrorSelector manifest for the given source and direction.
	mirrorSelectorYAML := func(name, sourceKind, sourceName, direction string) []byte {
		return []byte(fmt.Sprintf(`apiVersion: network.t-caas.telekom.com/v1alpha1
kind: MirrorSelector
metadata:
  name: %s
spec:
  trafficMatch:
    protocol: icmp
  mirrorTarget:
    apiGroup: network.t-caas.telekom.com
    kind: MirrorTarget
    name: collector-prod
  mirrorSource:
    apiGroup: network.t-caas.telekom.com
    kind: %s
    name: %q
  direction: %s
`, name, sourceKind, sourceName, direction))
	}

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		// The mirror VRF, collector VLAN 590 and MirrorTarget are created once in
		// the suite setup (see BeforeSuite) to avoid repeated VRF/VLAN churn that
		// would destabilise EVPN convergence for other tests. Only the lightweight
		// MirrorSelector is toggled per test.

		By("Applying mirror NAD for VLAN 590")
		nad, err := readTestdata("mirror/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())

		By("Applying L2 NAD for VLAN 501/502 (traffic source)")
		l2Nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, l2Nad, ns)).To(Succeed())
	})

	AfterEach(func() {
		By("Cleaning up test pods")
		_ = f.DeletePod(ctx, ns, "mirror-capture")
		_ = f.DeletePod(ctx, ns, "traffic-src")
		_ = f.DeletePod(ctx, ns, "traffic-dst")
	})

	// createTrafficPods creates the mirror-capture, traffic-src and traffic-dst pods.
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
	}

	// verifyMirrorCapture sends pings and verifies GRE packets are captured at the collector.
	verifyMirrorCapture := func(dstIP, label string) {
		By(fmt.Sprintf("Sending ICMP traffic from traffic-src → traffic-dst (%s)", label))
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "traffic-src", dstIP, 5)
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

	runMirrorCase := func(name, sourceKind, sourceName, direction, label string) {
		cfg := f.Config

		By(fmt.Sprintf("Applying %s MirrorSelector", label))
		sel := mirrorSelectorYAML(name, sourceKind, sourceName, direction)
		Expect(f.ApplyManifest(ctx, sel)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeleteManifest(context.Background(), sel)
		})

		By("Waiting for NodeNetworkConfig convergence")
		time.Sleep(15 * time.Second)

		createTrafficPods(cfg)
		verifyMirrorCapture(cfg.Macvlan02IPv4, label)
	}

	Context("Layer2 source", func() {
		It("should capture GRE packets with ingress-only mirror on VLAN 501", func() {
			runMirrorCase("mirror-l2-ingress", "Layer2NetworkConfiguration", "vlan1", "ingress", "L2 ingress-only")
		})

		It("should capture GRE packets with egress-only mirror on VLAN 501", func() {
			runMirrorCase("mirror-l2-egress", "Layer2NetworkConfiguration", "vlan1", "egress", "L2 egress-only")
		})
	})

	Context("VRF source", func() {
		It("should capture GRE packets with ingress-only mirror on m2m VRF", func() {
			runMirrorCase("mirror-vrf-ingress", "VRFRouteConfiguration", "m2m-test-vrf", "ingress", "VRF ingress-only")
		})

		It("should capture GRE packets with egress-only mirror on m2m VRF", func() {
			runMirrorCase("mirror-vrf-egress", "VRFRouteConfiguration", "m2m-test-vrf", "egress", "VRF egress-only")
		})
	})
})
