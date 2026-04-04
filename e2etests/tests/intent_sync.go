package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	syncNamespace = "cluster-nwop2"
	remoteNS      = "default"
	syncTimeout   = 120 * time.Second
	syncInterval  = 5 * time.Second
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
			Expect(labels).To(HaveKeyWithValue("network-sync.telekom.com/managed-by", "network-sync"))
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
})

// objectExistsOnCluster2 checks if a network-connector CRD exists on cluster-2.
func objectExistsOnCluster2(ctx context.Context, f *framework.Framework, resource, name string) bool {
	obj := getCluster2Object(ctx, f, resource, name)
	return obj != nil
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
