package tests

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Intent SBR NNC Validation — checks that Outbound/Inbound destination selectors
// produce correct SBR output: intermediate LocalVRFs, ClusterVRF PolicyRoutes,
// and combo VRF dedup for multi-VRF consumers.
var _ = Describe("Intent: SBR NNC Validation", Label("intent", "sbr", "nnc"), func() {
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

	Context("single-VRF Outbound → s-<vrf> intermediate", func() {
		It("should create LocalVRF s-m2m with static routes and ClusterVRF policy routes (v4+v6)", func() {
			cfg := f.Config

			By("Applying single-VRF SBR manifests (Outbound selecting m2m only)")
			manifest, err := readTestdata("intent/sbr-single/manifests.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

			By("Waiting for NNC spec to include LocalVRF s-m2m and both v4+v6 policy routes")
			var nnc *unstructured.Unstructured
			Eventually(func() bool {
				var getErr error
				nnc, getErr = f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil &&
					framework.NNCHasLocalVRF(nnc, "s-m2m") &&
					framework.NNCClusterVRFHasPolicyRoute(nnc, "10.250.4.40/32", "s-m2m") &&
					framework.NNCClusterVRFHasPolicyRoute(nnc, "fd94:685b:30cf:501::40/128", "s-m2m")
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should have LocalVRF 's-m2m' and v4+v6 policy routes")

			By("Verifying LocalVRF s-m2m has static routes to FabricVRF m2m (v4 + v6)")
			Expect(framework.NNCLocalVRFStaticRouteTarget(nnc, "s-m2m", "10.102.0.0/16", "m2m")).To(BeTrue(),
				"s-m2m should have static route 10.102.0.0/16 → m2m")
			Expect(framework.NNCLocalVRFStaticRouteTarget(nnc, "s-m2m", "fda5:25c1:193c::/48", "m2m")).To(BeTrue(),
				"s-m2m should have static route fda5:25c1:193c::/48 → m2m")

			By("Verifying policy routes have NO dst prefix (source-only matching)")
			Expect(framework.NNCClusterVRFPolicyRouteDstPrefix(nnc, "10.250.4.40/32")).To(BeNil(),
				"IPv4 policy route should be source-only")
			Expect(framework.NNCClusterVRFPolicyRouteDstPrefix(nnc, "fd94:685b:30cf:501::40/128")).To(BeNil(),
				"IPv6 policy route should be source-only")

			_ = f.DeleteManifest(ctx, manifest)
		})
	})

	Context("multi-VRF Outbound → combo intermediate VRF", func() {
		It("should create a hashed combo LocalVRF with routes to both fabric VRFs (v4+v6)", func() {
			cfg := f.Config

			By("Applying multi-VRF SBR manifests (Outbound selecting m2m + c2m)")
			manifest, err := readTestdata("intent/sbr-combo/manifests.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

			By("Waiting for NNC spec to include a combo LocalVRF")
			var nnc *unstructured.Unstructured
			var comboName string
			Eventually(func() bool {
				var getErr error
				nnc, getErr = f.GetNNC(ctx, cfg.WorkerNode1)
				if getErr != nil {
					return false
				}
				for _, name := range framework.NNCLocalVRFNames(nnc) {
					if strings.HasPrefix(name, "s-") && name != "s-m2m" && name != "s-c2m" {
						comboName = name
						return true
					}
				}
				return false
			}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should have a hashed combo LocalVRF")

			By("Verifying combo VRF has static routes to BOTH fabric VRFs (v4 + v6)")
			// dest-dcgw (m2m): 10.102.0.0/16, fda5:25c1:193c::/48
			Expect(framework.NNCLocalVRFStaticRouteTarget(nnc, comboName, "10.102.0.0/16", "m2m")).To(BeTrue(),
				"combo VRF should have 10.102.0.0/16 → m2m")
			Expect(framework.NNCLocalVRFStaticRouteTarget(nnc, comboName, "fda5:25c1:193c::/48", "m2m")).To(BeTrue(),
				"combo VRF should have fda5:25c1:193c::/48 → m2m")
			// dest-dcgw-c2m (c2m): 10.102.1.0/24, fda5:25c1:193d::/48
			Expect(framework.NNCLocalVRFStaticRouteTarget(nnc, comboName, "10.102.1.0/24", "c2m")).To(BeTrue(),
				"combo VRF should have 10.102.1.0/24 → c2m")
			Expect(framework.NNCLocalVRFStaticRouteTarget(nnc, comboName, "fda5:25c1:193d::/48", "c2m")).To(BeTrue(),
				"combo VRF should have fda5:25c1:193d::/48 → c2m")

			By("Verifying ClusterVRF has v4+v6 source-only policy routes to combo VRF")
			Expect(framework.NNCClusterVRFHasPolicyRoute(nnc, "10.250.4.50/32", comboName)).To(BeTrue(),
				"ClusterVRF should route IPv4 source → combo VRF")
			Expect(framework.NNCClusterVRFHasPolicyRoute(nnc, "fd94:685b:30cf:501::50/128", comboName)).To(BeTrue(),
				"ClusterVRF should route IPv6 source → combo VRF")

			By("Verifying policy routes have NO dst prefix (LPM inside combo VRF handles it)")
			Expect(framework.NNCClusterVRFPolicyRouteDstPrefix(nnc, "10.250.4.50/32")).To(BeNil(),
				"IPv4 combo policy route should be source-only")
			Expect(framework.NNCClusterVRFPolicyRouteDstPrefix(nnc, "fd94:685b:30cf:501::50/128")).To(BeNil(),
				"IPv6 combo policy route should be source-only")

			_ = f.DeleteManifest(ctx, manifest)
		})
	})

	Context("SBR lifecycle — add and remove consumer", func() {
		It("should add and remove v4+v6 policy routes when Outbound is created/deleted", func() {
			cfg := f.Config

			By("Ensuring SBR lifecycle manifests are not present")
			manifest, err := readTestdata("intent/sbr-lifecycle/manifests.yaml")
			Expect(err).NotTo(HaveOccurred())
			_ = f.DeleteManifest(ctx, manifest)

			By("Waiting for NNC spec to not contain our policy routes")
			Eventually(func() bool {
				nnc, getErr := f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil &&
					!framework.NNCClusterVRFHasPolicyRoute(nnc, "10.250.4.99/32", "s-m2m") &&
					!framework.NNCClusterVRFHasPolicyRoute(nnc, "fd94:685b:30cf:501::99/128", "s-m2m")
			}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should not contain policy routes for our addresses before test")

			By("Capturing NNC revision before adding Outbound")
			nncBefore, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			revBefore := framework.NNCRevision(nncBefore)

			By("Applying SBR single-VRF manifests")
			Expect(f.ApplyManifest(ctx, manifest)).To(Succeed())

			By("Waiting for NNC spec to include v4+v6 policy routes")
			Eventually(func() bool {
				nnc, getErr := f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil &&
					framework.NNCRevision(nnc) != revBefore &&
					framework.NNCClusterVRFHasPolicyRoute(nnc, "10.250.4.99/32", "s-m2m") &&
					framework.NNCClusterVRFHasPolicyRoute(nnc, "fd94:685b:30cf:501::99/128", "s-m2m")
			}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should have v4+v6 policy routes after Outbound creation")

			By("Capturing revision after create")
			nncAfterCreate, err := f.GetNNC(ctx, cfg.WorkerNode1)
			Expect(err).NotTo(HaveOccurred())
			revAfterCreate := framework.NNCRevision(nncAfterCreate)

			By("Deleting SBR manifests")
			Expect(f.DeleteManifest(ctx, manifest)).To(Succeed())

			By("Waiting for NNC spec to no longer contain our policy routes")
			Eventually(func() bool {
				nnc, getErr := f.GetNNC(ctx, cfg.WorkerNode1)
				return getErr == nil &&
					framework.NNCRevision(nnc) != revAfterCreate &&
					!framework.NNCClusterVRFHasPolicyRoute(nnc, "10.250.4.99/32", "s-m2m") &&
					!framework.NNCClusterVRFHasPolicyRoute(nnc, "fd94:685b:30cf:501::99/128", "s-m2m")
			}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
				"NNC spec should no longer have our policy routes after Outbound deletion")
		})
	})
})
