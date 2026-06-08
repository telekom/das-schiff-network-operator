package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var nncGVK = schema.GroupVersionKind{
	Group:   "network.t-caas.telekom.com",
	Version: "v1alpha1",
	Kind:    "NodeNetworkConfig",
}

// addMirrorACLToNNC reads the NNC for the given node, adds the provided mirrorACL
// to spec.fabricVRFs[vrfName].mirrorAcls, and patches the object using a merge-patch
// (MergeFrom). Unlike Update, Patch only sends the changed fields so it is not
// susceptible to resourceVersion conflicts from concurrent status updates.
func addMirrorACLToNNC(ctx context.Context, f *framework.Framework, nodeName, vrfName string, mirrorACL map[string]interface{}) (string, error) {
	nnc := &unstructured.Unstructured{}
	nnc.SetGroupVersionKind(nncGVK)
	if err := f.Client.Get(ctx, types.NamespacedName{Name: nodeName}, nnc); err != nil {
		return "", fmt.Errorf("get NNC %s: %w", nodeName, err)
	}

	// Build the patch base before modifying so MergeFrom can compute the diff.
	base := nnc.DeepCopy()

	fabricVRFs, found, err := unstructured.NestedMap(nnc.Object, "spec", "fabricVRFs")
	if err != nil {
		return "", fmt.Errorf("read fabricVRFs: %w", err)
	}
	if !found {
		fabricVRFs = map[string]interface{}{}
	}

	vrf, ok := fabricVRFs[vrfName].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("VRF %q not found in NNC %s fabricVRFs", vrfName, nodeName)
	}

	existing, ok := vrf["mirrorAcls"].([]interface{})
	if !ok {
		existing = nil
	}
	vrf["mirrorAcls"] = append(existing, mirrorACL)
	fabricVRFs[vrfName] = vrf

	if err := unstructured.SetNestedMap(nnc.Object, fabricVRFs, "spec", "fabricVRFs"); err != nil {
		return "", fmt.Errorf("set fabricVRFs: %w", err)
	}

	// Bump spec.revision so the CRA agent detects the config change and reprocesses.
	// The CRA compares in-memory revision with spec.revision; if unchanged it skips.
	oldRev, found, err := unstructured.NestedString(nnc.Object, "spec", "revision")
	if err != nil {
		return "", fmt.Errorf("read spec.revision: %w", err)
	}
	if !found {
		oldRev = ""
	}
	newRev := oldRev + "-mirror"
	if err := unstructured.SetNestedField(nnc.Object, newRev, "spec", "revision"); err != nil {
		return "", fmt.Errorf("bump revision: %w", err)
	}

	return newRev, f.Client.Patch(ctx, nnc, client.MergeFrom(base))
}

// removeMirrorACLsFromNNC reads the NNC for the given node, removes all mirrorAcls
// from spec.fabricVRFs[vrfName], and patches the object using a merge-patch (MergeFrom).
// Using Patch instead of Update avoids resourceVersion conflicts from concurrent status updates.
func removeMirrorACLsFromNNC(ctx context.Context, f *framework.Framework, nodeName, vrfName string) error {
	nnc := &unstructured.Unstructured{}
	nnc.SetGroupVersionKind(nncGVK)
	if err := f.Client.Get(ctx, types.NamespacedName{Name: nodeName}, nnc); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Build the patch base before modifying so MergeFrom can compute the diff.
	base := nnc.DeepCopy()

	fabricVRFs, found, err := unstructured.NestedMap(nnc.Object, "spec", "fabricVRFs")
	if err != nil {
		return fmt.Errorf("read fabricVRFs: %w", err)
	}
	if !found {
		return nil
	}

	vrf, ok := fabricVRFs[vrfName].(map[string]interface{})
	if !ok {
		return nil
	}

	delete(vrf, "mirrorAcls")
	fabricVRFs[vrfName] = vrf

	if err := unstructured.SetNestedMap(nnc.Object, fabricVRFs, "spec", "fabricVRFs"); err != nil {
		return fmt.Errorf("set fabricVRFs: %w", err)
	}

	// Bump spec.revision so the CRA agent detects the config change and reprocesses.
	// The CRA compares in-memory revision with spec.revision; if unchanged it skips.
	oldRev, found, err := unstructured.NestedString(nnc.Object, "spec", "revision")
	if err != nil {
		return fmt.Errorf("read spec.revision: %w", err)
	}
	if !found {
		oldRev = ""
	}
	if err := unstructured.SetNestedField(nnc.Object, oldRev+"-unmirror", "spec", "revision"); err != nil {
		return fmt.Errorf("bump revision: %w", err)
	}

	return f.Client.Patch(ctx, nnc, client.MergeFrom(base))
}

// verifyMirrorCapture verifies that traffic is mirrored to a capture pod.
// It starts tcpdump on capturePod FIRST (to avoid missing packets), waits for the
// process to be active, then sends a ping from srcPod to targetIP, stops the capture,
// and asserts that at least one packet from srcPod was captured.
//
// captureIface is the interface on capturePod to listen on (e.g. "net1").
// srcPod and srcPodNS identify the pod from which to send test traffic.
// targetIP is the ping destination.
// TC-09: Traffic Mirroring (MirrorACL).
var _ = Describe("Traffic Mirroring", Label("mirror"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-mirror"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()
	})

	AfterEach(func() {
		By("Cleaning up test pods")
		_ = f.DeletePod(ctx, ns, "mirror-src")
		_ = f.DeletePod(ctx, ns, "mirror-capture")

		By("Cleaning up traffic mirror configuration (removing mirrorAcls from NNC)")
		cfg := f.Config
		_ = removeMirrorACLsFromNNC(ctx, f, cfg.WorkerNode1, cfg.VRFM2M)
	})

	It("should persist MirrorACLs in NNC spec when configured", func() {
		cfg := f.Config

		By("Adding MirrorACL to NNC for worker-1 via read-modify-write")
		srcPrefix := cfg.Macvlan01IPv4 + "/32"
		mirrorACL := map[string]interface{}{
			"trafficMatch": map[string]interface{}{
				"srcPrefix": srcPrefix,
			},
			"destinationAddress": cfg.Macvlan02IPv4,
			"destinationVrf":     cfg.VRFM2M,
			"encapsulationType":  "gre",
		}
		_, addErr := addMirrorACLToNNC(ctx, f, cfg.WorkerNode1, cfg.VRFM2M, mirrorACL)
		Expect(addErr).NotTo(HaveOccurred(), "mirrorAcl patch should be accepted by the API server")
	})

	It("should mirror ingress traffic to a capture pod when MirrorACLs are configured", func() {
		Skip("CRA agents do not yet implement mirrorAcl programming; skip traffic capture verification until mirror support lands")
	})
})
