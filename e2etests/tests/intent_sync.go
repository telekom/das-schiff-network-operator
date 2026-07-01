package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

const (
	syncNamespace = "cluster-nwop2"
	remoteNS      = "default"
	syncTimeout   = 120 * time.Second
	syncInterval  = 5 * time.Second

	syncManagedByLabel = "network-sync.telekom.com/managed-by"
	syncManagedByValue = "network-sync"

	helmManagedByLabel        = "app.kubernetes.io/managed-by"
	fluxHelmNameLabel         = "helm.toolkit.fluxcd.io/name"
	fluxHelmNamespaceLabel    = "helm.toolkit.fluxcd.io/namespace"
	helmReleaseNameAnnotation = "meta.helm.sh/release-name"
	helmReleaseNamespaceAnn   = "meta.helm.sh/release-namespace"
)

// Intent Sync tests validate that the network-sync controller propagates
// intent CRDs from the management cluster (cluster-1, sync namespace)
// to the workload cluster (cluster-2, default namespace).
var _ = Describe("Intent Sync Controller", Label("intent", "sync"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()
	})

	Context("CRD sync from management to workload cluster", Ordered, func() {
		// Apply sync CRDs once for all tests in this context
		BeforeAll(func() {
			f = framework.Global
			Expect(f).NotTo(BeNil())
			ctx = context.Background()

			By("Applying sync intent CRDs in cluster-nwop2 namespace on mgmt cluster")
			syncCfg, err := readTestdata("intent/sync/cluster2-via-sync.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifestInNamespace(ctx, syncCfg, syncNamespace)).To(Succeed())

			By("Waiting for sync controller to propagate all CRDs")
			Eventually(func() bool {
				return objectExistsOnCluster2(ctx, f, "vrfs", "vrf-sync-m2m") &&
					objectExistsOnCluster2(ctx, f, "vrfs", "vrf-sync-c2m") &&
					objectExistsOnCluster2(ctx, f, "networks", "net-sync-vlan601") &&
					objectExistsOnCluster2(ctx, f, "layer2attachments", "l2a-sync-vlan601")
			}, syncTimeout, syncInterval).Should(BeTrue(), "Sync CRDs should appear on cluster-2")
		})

		AfterAll(func() {
			ctx = context.Background()
			By("Cleaning up sync intent CRDs from mgmt cluster")
			syncCfg, err := readTestdata("intent/sync/cluster2-via-sync.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.DeleteManifestInNamespace(ctx, syncCfg, syncNamespace)).To(Succeed())
		})

		It("should sync VRFs to the workload cluster", func() {
			By("Verifying VRFs exist on cluster-2")
			Expect(objectExistsOnCluster2(ctx, f, "vrfs", "vrf-sync-m2m")).To(BeTrue())
			Expect(objectExistsOnCluster2(ctx, f, "vrfs", "vrf-sync-c2m")).To(BeTrue())

			By("Verifying sync label on remote VRF")
			vrf := getCluster2Object(ctx, f, "vrfs", "vrf-sync-m2m")
			Expect(vrf).NotTo(BeNil())
			labels := vrf.GetLabels()
			Expect(labels).To(HaveKeyWithValue(syncManagedByLabel, syncManagedByValue))
		})

		It("should sync Networks and Layer2Attachments to the workload cluster", func() {
			By("Verifying Networks exist on cluster-2")
			Expect(objectExistsOnCluster2(ctx, f, "networks", "net-sync-vlan601")).To(BeTrue())
			Expect(objectExistsOnCluster2(ctx, f, "networks", "net-sync-vlan602")).To(BeTrue())

			By("Verifying Layer2Attachments exist on cluster-2")
			Expect(objectExistsOnCluster2(ctx, f, "layer2attachments", "l2a-sync-vlan601")).To(BeTrue())
			Expect(objectExistsOnCluster2(ctx, f, "layer2attachments", "l2a-sync-vlan602")).To(BeTrue())
		})

		It("should sync Destinations with labels preserved", func() {
			By("Verifying Destinations exist on cluster-2")
			Expect(objectExistsOnCluster2(ctx, f, "destinations", "dest-sync-m2m")).To(BeTrue())
			Expect(objectExistsOnCluster2(ctx, f, "destinations", "dest-sync-c2m")).To(BeTrue())

			By("Verifying labels are preserved on synced Destination")
			dest := getCluster2Object(ctx, f, "destinations", "dest-sync-m2m")
			Expect(dest).NotTo(BeNil())
			labels := dest.GetLabels()
			Expect(labels).To(HaveKeyWithValue("type", "sync-gateway-export"))
		})

		It("should propagate source-namespace annotation", func() {
			By("Checking source-namespace annotation on VRF")
			vrf := getCluster2Object(ctx, f, "vrfs", "vrf-sync-m2m")
			Expect(vrf).NotTo(BeNil())
			annotations := vrf.GetAnnotations()
			Expect(annotations).To(HaveKeyWithValue("network-sync.telekom.com/source-namespace", syncNamespace))
		})
	})

	Context("Deletion propagation", func() {
		It("should remove objects from workload cluster when deleted from mgmt cluster", func() {
			By("Applying a single VRF in the sync namespace")
			vrfYAML := `apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: VRF
metadata:
  name: vrf-sync-delete-test
spec:
  vrf: "sync-del"
  vni: 9999001
  routeTarget: "65188:9001"`
			Expect(f.ApplyManifestInNamespace(ctx, []byte(vrfYAML), syncNamespace)).To(Succeed())

			By("Waiting for VRF to be synced to cluster-2")
			Eventually(func() bool {
				return objectExistsOnCluster2(ctx, f, "vrfs", "vrf-sync-delete-test")
			}, syncTimeout, syncInterval).Should(BeTrue(), "VRF should appear on cluster-2")

			By("Deleting the VRF from the sync namespace")
			Expect(f.DeleteManifestInNamespace(ctx, []byte(vrfYAML), syncNamespace)).To(Succeed())

			By("Waiting for VRF to be removed from cluster-2")
			Eventually(func() bool {
				return !objectExistsOnCluster2(ctx, f, "vrfs", "vrf-sync-delete-test")
			}, syncTimeout, syncInterval).Should(BeTrue(), "VRF should be removed from cluster-2 after mgmt deletion")
		})
	})

	Context("Flux Helm ownership metadata", func() {
		const (
			vrfName         = "vrf-sync-flux-helm-ownership"
			sourceRelease   = "network-sync-source"
			workloadRelease = "network-sync-workload"
		)

		AfterEach(func() {
			ctx = context.Background()

			By("Cleaning up Flux HelmReleases")
			Expect(deleteFluxHelmRelease(ctx, f.Client, sourceRelease)).To(Succeed())
			Expect(deleteFluxHelmRelease(ctx, f.Cluster2Client(), workloadRelease)).To(Succeed())

			Expect(deleteObject(ctx, f.Client, "vrfs", syncNamespace, vrfName)).To(Succeed())
			Expect(deleteCluster2Object(ctx, f, "vrfs", vrfName)).To(Succeed())

			By("Cleaning up Flux chart repositories")
			Expect(cleanupFluxChartRepository(ctx, managementFluxCluster(f))).To(Succeed())
			Expect(cleanupFluxChartRepository(ctx, workloadFluxCluster(f))).To(Succeed())
		})

		It("should preserve actual Flux Helm workload ownership metadata while syncing Flux-managed source changes", Label("ownership", "helm", "flux"), func() {
			By("Installing Flux controllers on the management and workload clusters")
			Expect(ensureFluxInstalled(ctx, managementFluxCluster(f))).To(Succeed())
			Expect(ensureFluxInstalled(ctx, workloadFluxCluster(f))).To(Succeed())

			By("Serving the Helm fixture chart inside both clusters")
			Expect(ensureFluxChartRepository(ctx, managementFluxCluster(f))).To(Succeed())
			Expect(ensureFluxChartRepository(ctx, workloadFluxCluster(f))).To(Succeed())

			By("Creating the workload VRF through a real Flux HelmRelease")
			Expect(f.ApplyManifestToCluster2(ctx, []byte(fluxHelmReleaseYAML(workloadRelease, remoteNS, vrfName, 2002040, true)))).To(Succeed())

			Eventually(func() error {
				vrf := getCluster2Object(ctx, f, "vrfs", vrfName)
				if vrf == nil {
					return fmt.Errorf("workload VRF %q does not exist", vrfName)
				}
				if err := expectSyncedVRF(vrf, 2002040); err != nil {
					return err
				}
				return expectHelmFluxOwnership(vrf, workloadRelease, remoteNS)
			}, syncTimeout, syncInterval).Should(Succeed())

			By("Creating the source VRF through a real Flux HelmRelease")
			Expect(f.ApplyManifest(ctx, []byte(fluxHelmReleaseYAML(sourceRelease, syncNamespace, vrfName, 2002041, false)))).To(Succeed())

			By("Waiting for Flux to create the source VRF with Helm ownership metadata")
			Eventually(func() error {
				vrf := getObject(ctx, f.Client, "vrfs", syncNamespace, vrfName)
				if vrf == nil {
					return fmt.Errorf("source VRF %q does not exist", vrfName)
				}
				if err := expectVRFVNI(vrf, 2002041); err != nil {
					return err
				}
				return expectHelmFluxOwnership(vrf, sourceRelease, syncNamespace)
			}, syncTimeout, syncInterval).Should(Succeed())

			By("Verifying network-sync updates the Flux-owned workload object without stealing ownership")
			Eventually(func() error {
				vrf := getCluster2Object(ctx, f, "vrfs", vrfName)
				if vrf == nil {
					return fmt.Errorf("workload VRF %q does not exist", vrfName)
				}
				if err := expectSyncedVRF(vrf, 2002041); err != nil {
					return err
				}
				return expectHelmFluxOwnership(vrf, workloadRelease, remoteNS)
			}, syncTimeout, syncInterval).Should(Succeed())

			By("Forcing workload Flux to reconcile after network-sync mutates the workload VRF")
			requestedAt, err := requestFluxHelmReleaseReconcile(ctx, f.Cluster2Client(), workloadRelease)
			Expect(err).NotTo(HaveOccurred())
			Expect(waitForFluxHelmReleaseReconcile(ctx, f.Cluster2Client(), workloadRelease, requestedAt)).To(Succeed())

			By("Checking the workload VRF remains converged after the workload Flux reconcile")
			Eventually(func() error {
				vrf := getCluster2Object(ctx, f, "vrfs", vrfName)
				if vrf == nil {
					return fmt.Errorf("workload VRF %q does not exist", vrfName)
				}
				if err := expectSyncedVRF(vrf, 2002041); err != nil {
					return err
				}
				return expectHelmFluxOwnership(vrf, workloadRelease, remoteNS)
			}, syncTimeout, syncInterval).Should(Succeed())
			Consistently(func() error {
				vrf := getCluster2Object(ctx, f, "vrfs", vrfName)
				if vrf == nil {
					return fmt.Errorf("workload VRF %q does not exist", vrfName)
				}
				if err := expectSyncedVRF(vrf, 2002041); err != nil {
					return err
				}
				return expectHelmFluxOwnership(vrf, workloadRelease, remoteNS)
			}, 30*time.Second, syncInterval).Should(Succeed())

			By("Updating the source HelmRelease values through Flux")
			Expect(f.ApplyManifest(ctx, []byte(fluxHelmReleaseYAML(sourceRelease, syncNamespace, vrfName, 2002042, false)))).To(Succeed())

			By("Waiting for Flux to update the source VRF")
			Eventually(func() error {
				vrf := getObject(ctx, f.Client, "vrfs", syncNamespace, vrfName)
				if vrf == nil {
					return fmt.Errorf("source VRF %q does not exist", vrfName)
				}
				if err := expectVRFVNI(vrf, 2002042); err != nil {
					return err
				}
				return expectHelmFluxOwnership(vrf, sourceRelease, syncNamespace)
			}, syncTimeout, syncInterval).Should(Succeed())

			By("Checking workload Flux ownership survives the Flux-managed source update")
			Eventually(func() error {
				vrf := getCluster2Object(ctx, f, "vrfs", vrfName)
				if vrf == nil {
					return fmt.Errorf("workload VRF %q does not exist", vrfName)
				}
				if err := expectSyncedVRF(vrf, 2002042); err != nil {
					return err
				}
				return expectHelmFluxOwnership(vrf, workloadRelease, remoteNS)
			}, syncTimeout, syncInterval).Should(Succeed())
		})
	})
})

// objectExistsOnCluster2 checks if a network-connector CRD exists on cluster-2.
func objectExistsOnCluster2(ctx context.Context, f *framework.Framework, resource, name string) bool {
	obj := getCluster2Object(ctx, f, resource, name)
	return obj != nil
}

func deleteCluster2Object(ctx context.Context, f *framework.Framework, resource, name string) error {
	return deleteObject(ctx, f.Cluster2Client(), resource, remoteNS, name)
}

// getCluster2Object fetches a network-connector CRD from cluster-2's default namespace.
func getCluster2Object(ctx context.Context, f *framework.Framework, resource, name string) *unstructured.Unstructured {
	return getObject(ctx, f.Cluster2Client(), resource, remoteNS, name)
}

func getObject(ctx context.Context, c client.Client, resource, namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "network-connector.sylvaproject.org",
		Version: "v1alpha1",
		Kind:    resourceToKind(resource),
	})
	err := c.Get(ctx, framework.ObjectKey(namespace, name), obj)
	if err != nil {
		return nil
	}
	return obj
}

func deleteObject(ctx context.Context, c client.Client, resource, namespace, name string) error {
	obj := getObject(ctx, c, resource, namespace, name)
	if obj == nil {
		return nil
	}
	if err := client.IgnoreNotFound(c.Delete(ctx, obj)); err != nil {
		return err
	}
	return waitForObjectDeleted(ctx, c, obj, syncTimeout)
}

func resourceToKind(resource string) string {
	switch resource {
	case "vrfs":
		return "VRF"
	case "networks":
		return "Network"
	case "destinations":
		return "Destination"
	case "layer2attachments":
		return "Layer2Attachment"
	case "inbounds":
		return "Inbound"
	case "outbounds":
		return "Outbound"
	default:
		return resource
	}
}

func expectSyncedVRF(vrf *unstructured.Unstructured, expectedVNI int64) error {
	labels := vrf.GetLabels()
	if labels[syncManagedByLabel] != syncManagedByValue {
		return fmt.Errorf("expected sync label %s=%s, got %v", syncManagedByLabel, syncManagedByValue, labels)
	}
	return expectVRFVNI(vrf, expectedVNI)
}

func expectVRFVNI(vrf *unstructured.Unstructured, expectedVNI int64) error {
	vni, found, err := unstructured.NestedInt64(vrf.Object, "spec", "vni")
	if err != nil {
		return fmt.Errorf("read spec.vni: %w", err)
	}
	if !found {
		return fmt.Errorf("spec.vni is missing")
	}
	if vni != expectedVNI {
		return fmt.Errorf("expected spec.vni %d, got %d", expectedVNI, vni)
	}
	return nil
}

func expectHelmFluxOwnership(obj *unstructured.Unstructured, releaseName, targetNamespace string) error {
	labels := obj.GetLabels()
	if labels[helmManagedByLabel] != "Helm" ||
		labels[fluxHelmNameLabel] != releaseName ||
		labels[fluxHelmNamespaceLabel] != fluxSystemNamespace {
		return fmt.Errorf("expected Helm/Flux labels for %s/%s, got %v", fluxSystemNamespace, releaseName, labels)
	}

	annotations := obj.GetAnnotations()
	if annotations[helmReleaseNameAnnotation] != releaseName ||
		annotations[helmReleaseNamespaceAnn] != targetNamespace {
		return fmt.Errorf("expected Helm annotations for %s in target namespace %s, got %v", releaseName, targetNamespace, annotations)
	}
	return nil
}
