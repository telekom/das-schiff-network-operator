package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent Destination NNC Validation — checks that Destinations produce correct
// NNC output: FabricVRFs exist, VRFImports are present, multiple destinations
// merge cleanly, and destination lifecycle (add → delete) updates NNCs.
var _ = Describe("Intent: Destination NNC Validation", Label("intent", "destination", "nnc"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Applying intent base configs")
		base, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, base)).To(Succeed())
	})

	Context("single destination → NNC FabricVRF", func() {
		It("should create FabricVRF with VRFImport when L2A references destination", func() {
			cfg := f.Config

			By("Applying L2A targeting dest-dcgw (m2m)")
			manifest, err := readTestdata("intent/l2/manifests.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

			By("Waiting for NNC spec to include FabricVRF m2m")
			var nnc *unstructured.Unstructured
			Eventually(func() bool {
				var getErr error
				nnc, getErr = f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil && framework.NNCHasFabricVRF(nnc, "m2m")
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should contain fabricVRF 'm2m'")

			By("Verifying FabricVRF has VRFImport from cluster VRF and aggregate routes")
			Expect(framework.NNCFabricVRFHasVRFImport(nnc, "m2m", "cluster")).To(BeTrue(),
				"FabricVRF m2m should import from cluster VRF")
			Expect(framework.NNCFabricVRFHasAggregateRoute(nnc, "m2m", "10.250.0.1/24")).To(BeTrue(),
				"FabricVRF m2m should have aggregate static route for IPv4 Network CIDR")
			Expect(framework.NNCFabricVRFHasAggregateRoute(nnc, "m2m", "fd94:685b:30cf:501::1/64")).To(BeTrue(),
				"FabricVRF m2m should have aggregate static route for IPv6 Network CIDR")

			By("Checking NNC has Layer2 entry")
			Expect(framework.NNCHasLayer2(nnc, "501")).To(BeTrue(),
				"NNC should have layer2 '501' for VLAN 501")

			_ = f.DeleteManifest(ctx, manifest)
		})
	})

	Context("multiple destinations in same VRF", func() {
		It("should merge routes from both destinations without conflicts", func() {
			cfg := f.Config

			By("Applying L2A + second destination (both m2m VRF)")
			manifest, err := readTestdata("intent/destination-multi/manifests.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

			By("Waiting for NNC spec to include FabricVRF m2m")
			Eventually(func() bool {
				nnc, getErr := f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil && framework.NNCHasFabricVRF(nnc, "m2m")
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should contain fabricVRF 'm2m' with merged routes")

			_ = f.DeleteManifest(ctx, manifest)
		})
	})

	Context("destination lifecycle — add and remove", func() {
		It("should update NNC when L2A is added and cleaned up when deleted", func() {
			cfg := f.Config

			By("Ensuring lifecycle L2A does not exist before test")
			cleanupManifest, err := readTestdata("intent/destination-lifecycle/manifests.yaml")
			Expect(err).NotTo(HaveOccurred())
			_ = f.DeleteManifest(ctx, cleanupManifest)

			By("Waiting for NNC spec to not have Layer2 504")
			Eventually(func() bool {
				nnc, getErr := f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil && !framework.NNCHasLayer2(nnc, "504")
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should not have Layer2 504 before test")

			By("Capturing NNC state before adding L2A")
			_, err = f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())

			By("Applying lifecycle L2A for VLAN 504 (m2m VRF)")
			Expect(f.ApplyManifest(ctx, cleanupManifest)).To(Succeed())

			By("Waiting for NNC spec to include Layer2 504")
			Eventually(func() bool {
				nnc, getErr := f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil && framework.NNCHasLayer2(nnc, "504")
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should have Layer2 '504' after L2A creation")

			By("Verifying Layer2 504 exists in NNC spec")
			nncAfterCreate, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.NNCHasLayer2(nncAfterCreate, "504")).To(BeTrue(),
				"NNC should have Layer2 '504' after L2A creation")

			By("Deleting the lifecycle L2A")
			Expect(f.DeleteManifest(ctx, cleanupManifest)).To(Succeed())

			By("Waiting for NNC spec to not have Layer2 504")
			Eventually(func() bool {
				nnc, getErr := f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil && !framework.NNCHasLayer2(nnc, "504")
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should not have Layer2 '504' after L2A deletion")

			By("Verifying Layer2 504 is removed from NNC")
			nncAfterDelete, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.NNCHasLayer2(nncAfterDelete, "504")).To(BeFalse(),
				"Layer2 '504' should be removed after L2A deletion")
		})
	})

	Context("both VRFs coexist in same NNC", func() {
		It("should have both m2m and c2m FabricVRFs when using both VRFs", func() {
			cfg := f.Config

			By("Applying L2As for both VRFs")
			manifest, err := readTestdata("intent/vrf/manifests.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

			By("Waiting for NNC spec to include both FabricVRFs")
			var nnc *unstructured.Unstructured
			Eventually(func() bool {
				var getErr error
				nnc, getErr = f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil &&
					framework.NNCHasFabricVRF(nnc, "m2m") && framework.NNCHasFabricVRF(nnc, "c2m")
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should contain both fabricVRFs")

			By("Verifying both Layer2s present")
			Expect(framework.NNCHasLayer2(nnc, "501")).To(BeTrue())
			Expect(framework.NNCHasLayer2(nnc, "503")).To(BeTrue())

			_ = f.DeleteManifest(ctx, manifest)
		})
	})

	Context("multiple destinations in different VRFs", func() {
		It("should produce separate FabricVRFs for each destination's VRF", func() {
			cfg := f.Config

			By("Applying L2As targeting destinations in m2m and c2m VRFs + extra c2m dest")
			manifest, err := readTestdata("intent/destination-cross-vrf/manifests.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

			By("Waiting for NNC spec to include both FabricVRFs")
			var nnc *unstructured.Unstructured
			Eventually(func() bool {
				var getErr error
				nnc, getErr = f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil &&
					framework.NNCHasFabricVRF(nnc, "m2m") && framework.NNCHasFabricVRF(nnc, "c2m")
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should contain both FabricVRFs")

			By("Verifying Layer2 entries for both VLANs")
			Expect(framework.NNCHasLayer2(nnc, "501")).To(BeTrue(),
				"NNC should have Layer2 for VLAN 501 (m2m)")
			Expect(framework.NNCHasLayer2(nnc, "503")).To(BeTrue(),
				"NNC should have Layer2 for VLAN 503 (c2m)")

			By("Verifying VRFImport from cluster exists in both VRFs with aggregate routes")
			Expect(framework.NNCFabricVRFHasVRFImport(nnc, "m2m", "cluster")).To(BeTrue(),
				"FabricVRF m2m should import from cluster VRF")
			Expect(framework.NNCFabricVRFHasVRFImport(nnc, "c2m", "cluster")).To(BeTrue(),
				"FabricVRF c2m should import from cluster VRF")
			Expect(framework.NNCFabricVRFHasAggregateRoute(nnc, "m2m", "10.250.0.1/24")).To(BeTrue(),
				"FabricVRF m2m should have aggregate route for net-vlan501 IPv4 CIDR")
			Expect(framework.NNCFabricVRFHasAggregateRoute(nnc, "c2m", "10.250.30.1/24")).To(BeTrue(),
				"FabricVRF c2m should have aggregate route for net-vlan503 IPv4 CIDR")

			_ = f.DeleteManifest(ctx, manifest)
		})
	})
})
