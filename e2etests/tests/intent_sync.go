package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

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
		const vrfName = "vrf-sync-helm-flux-ownership"

		AfterEach(func() {
			ctx = context.Background()
			defer func() {
				Expect(deleteCluster2Object(ctx, f, "vrfs", vrfName)).To(Succeed())
			}()

			By("Cleaning up simulated Helm-owned sync VRF from mgmt cluster")
			Expect(f.DeleteManifestInNamespace(ctx, []byte(helmOwnedSourceVRFUpdatedYAML), syncNamespace)).To(Succeed())

			By("Waiting for simulated Helm-owned sync VRF to be removed from cluster-2")
			Eventually(func() bool {
				return !objectExistsOnCluster2(ctx, f, "vrfs", vrfName)
			}, syncTimeout, syncInterval).Should(BeTrue())
		})

		It("should preserve simulated Flux Helm workload ownership metadata while syncing source changes", Label("ownership", "helm", "flux"), func() {
			// The e2e lab does not install Flux Helm controllers. This simulates
			// the metadata those controllers apply, then forces network-sync
			// through its update path to catch ownership fights.
			By("Applying a source VRF with Helm/Flux ownership metadata in the sync namespace")
			Expect(f.ApplyManifestInNamespace(ctx, []byte(helmOwnedSourceVRFInitialYAML), syncNamespace)).To(Succeed())

			By("Waiting for network-sync to create the workload VRF without copying source Helm ownership")
			Eventually(func() error {
				vrf := getCluster2Object(ctx, f, "vrfs", vrfName)
				if vrf == nil {
					return fmt.Errorf("workload VRF %q does not exist", vrfName)
				}
				if err := expectSyncedVRF(vrf, 2002040); err != nil {
					return err
				}
				return expectNoHelmFluxOwnership(vrf)
			}, syncTimeout, syncInterval).Should(Succeed())

			By("Simulating Flux Helm ownership on the workload-cluster object")
			Eventually(func() error {
				vrf := getCluster2Object(ctx, f, "vrfs", vrfName)
				if vrf == nil {
					return fmt.Errorf("workload VRF %q does not exist", vrfName)
				}
				setHelmFluxOwnership(vrf, "workload-networking", "workload-flux-system")
				return f.Cluster2Client().Update(ctx, vrf)
			}, syncTimeout, syncInterval).Should(Succeed())

			By("Updating the source VRF spec to force a network-sync reconciliation")
			Expect(f.ApplyManifestInNamespace(ctx, []byte(helmOwnedSourceVRFUpdatedYAML), syncNamespace)).To(Succeed())

			By("Verifying sync updates spec drift while preserving workload Helm ownership")
			Eventually(func() error {
				vrf := getCluster2Object(ctx, f, "vrfs", vrfName)
				if vrf == nil {
					return fmt.Errorf("workload VRF %q does not exist", vrfName)
				}
				if err := expectSyncedVRF(vrf, 2002041); err != nil {
					return err
				}
				return expectHelmFluxOwnership(vrf, "workload-networking", "workload-flux-system")
			}, syncTimeout, syncInterval).Should(Succeed())

			By("Triggering another source reconciliation with the same spec")
			Expect(f.ApplyManifestInNamespace(ctx, []byte(helmOwnedSourceVRFUpdatedYAML), syncNamespace)).To(Succeed())

			By("Checking ownership metadata survives the subsequent sync")
			Eventually(func() error {
				vrf := getCluster2Object(ctx, f, "vrfs", vrfName)
				if vrf == nil {
					return fmt.Errorf("workload VRF %q does not exist", vrfName)
				}
				if err := expectSyncedVRF(vrf, 2002041); err != nil {
					return err
				}
				return expectHelmFluxOwnership(vrf, "workload-networking", "workload-flux-system")
			}, syncTimeout, syncInterval).Should(Succeed())
		})
	})
})

const helmOwnedSourceVRFInitialYAML = `apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: VRF
metadata:
  name: vrf-sync-helm-flux-ownership
  labels:
    app.kubernetes.io/managed-by: Helm
    helm.toolkit.fluxcd.io/name: source-networking
    helm.toolkit.fluxcd.io/namespace: source-flux-system
  annotations:
    meta.helm.sh/release-name: source-networking
    meta.helm.sh/release-namespace: source-flux-system
spec:
  vrf: "ownmeta"
  vni: 2002040
  routeTarget: "65188:2040"`

const helmOwnedSourceVRFUpdatedYAML = `apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: VRF
metadata:
  name: vrf-sync-helm-flux-ownership
  labels:
    app.kubernetes.io/managed-by: Helm
    helm.toolkit.fluxcd.io/name: source-networking
    helm.toolkit.fluxcd.io/namespace: source-flux-system
  annotations:
    meta.helm.sh/release-name: source-networking
    meta.helm.sh/release-namespace: source-flux-system
spec:
  vrf: "ownmeta"
  vni: 2002041
  routeTarget: "65188:2041"`

// objectExistsOnCluster2 checks if a network-connector CRD exists on cluster-2.
func objectExistsOnCluster2(ctx context.Context, f *framework.Framework, resource, name string) bool {
	obj := getCluster2Object(ctx, f, resource, name)
	return obj != nil
}

func deleteCluster2Object(ctx context.Context, f *framework.Framework, resource, name string) error {
	obj := getCluster2Object(ctx, f, resource, name)
	if obj == nil {
		return nil
	}
	if err := f.Cluster2Client().Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// getCluster2Object fetches a network-connector CRD from cluster-2's default namespace.
func getCluster2Object(ctx context.Context, f *framework.Framework, resource, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "network-connector.sylvaproject.org",
		Version: "v1alpha1",
		Kind:    resourceToKind(resource),
	})
	err := f.Cluster2Client().Get(ctx, framework.ObjectKey(remoteNS, name), obj)
	if err != nil {
		return nil
	}
	return obj
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

func setHelmFluxOwnership(obj *unstructured.Unstructured, releaseName, releaseNamespace string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[helmManagedByLabel] = "Helm"
	labels[fluxHelmNameLabel] = releaseName
	labels[fluxHelmNamespaceLabel] = releaseNamespace
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[helmReleaseNameAnnotation] = releaseName
	annotations[helmReleaseNamespaceAnn] = releaseNamespace
	obj.SetAnnotations(annotations)
}

func expectHelmFluxOwnership(obj *unstructured.Unstructured, releaseName, releaseNamespace string) error {
	labels := obj.GetLabels()
	if labels[helmManagedByLabel] != "Helm" ||
		labels[fluxHelmNameLabel] != releaseName ||
		labels[fluxHelmNamespaceLabel] != releaseNamespace {
		return fmt.Errorf("expected workload Helm/Flux labels for %s/%s, got %v", releaseNamespace, releaseName, labels)
	}

	annotations := obj.GetAnnotations()
	if annotations[helmReleaseNameAnnotation] != releaseName ||
		annotations[helmReleaseNamespaceAnn] != releaseNamespace {
		return fmt.Errorf("expected workload Helm annotations for %s/%s, got %v", releaseNamespace, releaseName, annotations)
	}
	return nil
}

func expectNoHelmFluxOwnership(obj *unstructured.Unstructured) error {
	for _, key := range []string{helmManagedByLabel, fluxHelmNameLabel, fluxHelmNamespaceLabel} {
		if value, ok := obj.GetLabels()[key]; ok {
			return fmt.Errorf("expected ownership label %q to be absent, got %q", key, value)
		}
	}
	for _, key := range []string{helmReleaseNameAnnotation, helmReleaseNamespaceAnn} {
		if value, ok := obj.GetAnnotations()[key]; ok {
			return fmt.Errorf("expected ownership annotation %q to be absent, got %q", key, value)
		}
	}
	return nil
}
