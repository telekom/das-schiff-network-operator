package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

			By("Waiting for NNC to reconcile")
			time.Sleep(15 * time.Second)

			By("Checking NNC on worker-1 has FabricVRF for m2m")
			nnc, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.NNCHasFabricVRF(nnc, "vrf-m2m")).To(BeTrue(),
				"NNC should have fabricVRF 'm2m' from dest-dcgw → vrf-m2m")

			By("Verifying FabricVRF has VRFImport from cluster VRF")
			Expect(framework.NNCFabricVRFHasVRFImport(nnc, "vrf-m2m", "cluster")).To(BeTrue(),
				"FabricVRF m2m should import from cluster VRF")

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

			By("Waiting for NNC to reconcile")
			time.Sleep(15 * time.Second)

			By("Checking NNC on worker-1 has FabricVRF for m2m")
			nnc, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.NNCHasFabricVRF(nnc, "vrf-m2m")).To(BeTrue(),
				"NNC should have fabricVRF 'm2m' with merged routes from both destinations")

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
			// Wait for any prior cleanup to settle.
			time.Sleep(10 * time.Second)

			By("Capturing NNC state before adding L2A")
			nncBefore, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			revBefore := framework.NNCRevision(nncBefore)

			By("Applying lifecycle L2A for VLAN 504 (m2m VRF)")
			Expect(f.ApplyManifest(ctx, cleanupManifest)).To(Succeed())

			By("Waiting for NNC revision to change (L2A added)")
			Eventually(func() bool {
				nnc, err := f.GetNNC(ctx, cfg.WorkerNode1)
				if err != nil {
					return false
				}
				return framework.NNCRevision(nnc) != revBefore
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC revision should change after L2A creation")

			By("Capturing post-creation state")
			nncAfterCreate, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.NNCHasLayer2(nncAfterCreate, "504")).To(BeTrue(),
				"NNC should have Layer2 '504' after L2A creation")
			revAfterCreate := framework.NNCRevision(nncAfterCreate)

			By("Deleting the lifecycle L2A")
			Expect(f.DeleteManifest(ctx, cleanupManifest)).To(Succeed())

			By("Waiting for NNC revision to change again (L2A removed)")
			Eventually(func() bool {
				nnc, err := f.GetNNC(ctx, cfg.WorkerNode1)
				if err != nil {
					return false
				}
				return framework.NNCRevision(nnc) != revAfterCreate
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC revision should change after L2A deletion")

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

			By("Waiting for NNC to reconcile")
			time.Sleep(15 * time.Second)

			By("Checking NNC has both FabricVRFs")
			nnc, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.NNCHasFabricVRF(nnc, "vrf-m2m")).To(BeTrue(),
				"NNC should have fabricVRF 'm2m'")
			Expect(framework.NNCHasFabricVRF(nnc, "vrf-c2m")).To(BeTrue(),
				"NNC should have fabricVRF 'c2m'")

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

			By("Waiting for NNC to reconcile")
			time.Sleep(15 * time.Second)

			By("Checking NNC has FabricVRF for both m2m and c2m")
			nnc, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.NNCHasFabricVRF(nnc, "vrf-m2m")).To(BeTrue(),
				"NNC should have fabricVRF 'vrf-m2m' from dest-dcgw")
			Expect(framework.NNCHasFabricVRF(nnc, "vrf-c2m")).To(BeTrue(),
				"NNC should have fabricVRF 'vrf-c2m' from dest-dcgw-c2m + dest-extra-c2m")

			By("Verifying Layer2 entries for both VLANs")
			Expect(framework.NNCHasLayer2(nnc, "501")).To(BeTrue(),
				"NNC should have Layer2 for VLAN 501 (m2m)")
			Expect(framework.NNCHasLayer2(nnc, "503")).To(BeTrue(),
				"NNC should have Layer2 for VLAN 503 (c2m)")

			By("Verifying VRFImport from cluster exists in both VRFs")
			Expect(framework.NNCFabricVRFHasVRFImport(nnc, "vrf-m2m", "cluster")).To(BeTrue(),
				"FabricVRF vrf-m2m should import from cluster VRF")
			Expect(framework.NNCFabricVRFHasVRFImport(nnc, "vrf-c2m", "cluster")).To(BeTrue(),
				"FabricVRF vrf-c2m should import from cluster VRF")

			_ = f.DeleteManifest(ctx, manifest)
		})
	})
})
