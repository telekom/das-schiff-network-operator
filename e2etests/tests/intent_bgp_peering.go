package tests

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent-based BGP Peering test using BGPPeering, Layer2Attachment, and Inbound CRDs.
var _ = Describe("Intent BGP Peering", Label("intent", "bgp"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-intent-bgp"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying intent base configs (VRFs, Networks, Destinations)")
		baseManifest, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, baseManifest)).To(Succeed())

		By("Applying intent BGP manifests (L2A + Inbound + BGPPeering)")
		manifest, err := readTestdata("intent/bgp/manifests.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

		By("Applying Bird pod manifests (ConfigMap + NAD)")
		birdManifest, err := readTestdata("intent/bgp/bird-pod.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, birdManifest, ns)).To(Succeed())

		By("Creating Bird BGP speaker pod on worker-1")
		cfg := f.Config
		Expect(f.CreateBirdPod(ctx, ns, "macvlan-intent-bgp", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": `[{"name": "macvlan-intent-bgp", "ips": ["10.250.0.50/24", "fd94:685b:30cf:501::50/64"]}]`,
		})).To(Succeed())

		By("Waiting for Bird pod to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "macvlan-intent-bgp", cfg.PodReadyTimeout)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "macvlan-intent-bgp")
		birdManifest, _ := readTestdata("intent/bgp/bird-pod.yaml")
		_ = f.DeleteManifest(ctx, birdManifest)
		manifest, _ := readTestdata("intent/bgp/manifests.yaml")
		_ = f.DeleteManifest(ctx, manifest)
		baseManifest, _ := readTestdata("intent/base-configs.yaml")
		_ = f.DeleteManifest(ctx, baseManifest)
	})

	It("should establish BGP session and propagate routes via intent CRDs", func() {
		cfg := f.Config

		By("Waiting for IPv4 BGP peering to be established in m2m VRF")
		Eventually(func() bool {
			summary, err := f.GetBGPSummaryOnKindNodeVRF(ctx, cfg.WorkerNode1, cfg.VRFM2M)
			if err != nil {
				return false
			}
			if summary.IPv4Unicast != nil {
				return framework.CountEstablishedPeers(summary.IPv4Unicast) > 0
			}
			return false
		}).WithTimeout(cfg.BGPTimeout).WithPolling(10*time.Second).Should(BeTrue(),
			"Intent BGP IPv4 peer did not reach Established state")

		By("Waiting for IPv6 BGP peering to be established in m2m VRF")
		Eventually(func() bool {
			summary, err := f.GetBGPSummaryOnKindNodeVRF(ctx, cfg.WorkerNode1, cfg.VRFM2M)
			if err != nil {
				return false
			}
			if summary.IPv6Unicast != nil {
				return framework.CountEstablishedPeers(summary.IPv6Unicast) > 0
			}
			return false
		}).WithTimeout(cfg.BGPTimeout).WithPolling(10*time.Second).Should(BeTrue(),
			"Intent BGP IPv6 peer did not reach Established state")

		By("Verifying BGP-imported IPv4 routes appear in m2m VRF routing table")
		Eventually(func() bool {
			output, err := f.VtyshExecOnKindNode(ctx, cfg.WorkerNode1,
				"show ip route vrf m2m")
			if err != nil {
				return false
			}
			return strings.Contains(output, "10.250.3")
		}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"Intent BGP IPv4 routes not found in m2m VRF routing table")

		By("Verifying BGP-imported IPv6 routes appear in m2m VRF routing table")
		Eventually(func() bool {
			output, err := f.VtyshExecOnKindNode(ctx, cfg.WorkerNode1,
				"show ipv6 route vrf m2m")
			if err != nil {
				return false
			}
			return strings.Contains(output, "fd75:2d70:f7f7")
		}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"Intent BGP IPv6 routes not found in m2m VRF routing table")

		By("Verifying Bird received routes from CRA-FRR")
		Eventually(func() bool {
			stdout, _, err := f.ExecInPod(ctx, ns, "macvlan-intent-bgp", "bird",
				[]string{"birdc", "show", "route"})
			if err != nil {
				return false
			}
			return strings.Contains(stdout, "10.250.0.0/24") || strings.Contains(stdout, "10.250.")
		}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"Bird did not receive exported routes from CRA-FRR")
	})
})
