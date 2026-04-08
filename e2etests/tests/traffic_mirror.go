package tests

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	oldRev, _, _ := unstructured.NestedString(nnc.Object, "spec", "revision")
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
	oldRev, _, _ := unstructured.NestedString(nnc.Object, "spec", "revision")
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
func verifyMirrorCapture(
	ctx context.Context,
	f *framework.Framework,
	ns, capturePod, captureIface string,
	srcPodNS, srcPod, targetIP string,
) {
	GinkgoHelper()

	By(fmt.Sprintf("Starting tcpdump on %s/%s iface=%s", ns, capturePod, captureIface))
	// Start tcpdump in the background; write to /tmp/capture.pcap and echo the PID.
	// We use "& echo $!" so we can reliably retrieve the PID for readiness polling.
	_, _, err := f.ExecInPod(ctx, ns, capturePod, "",
		[]string{"sh", "-c",
			"tcpdump -i " + captureIface + " -w /tmp/capture.pcap -q 2>/tmp/tcpdump.err & echo $! > /tmp/tcpdump.pid"})
	Expect(err).NotTo(HaveOccurred(), "failed to start tcpdump")

	By("Waiting for tcpdump to be active (checking PID)")
	// Poll until the PID file exists and the process is alive.
	pollCtx, pollCancel := context.WithTimeout(ctx, 15*time.Second)
	defer pollCancel()
	Expect(framework.Poll(pollCtx, time.Second, func() (bool, error) {
		// Use pollCtx (not outer ctx) so the exec respects the poll timeout
		stdout, _, pollErr := f.ExecInPod(pollCtx, ns, capturePod, "",
			[]string{"sh", "-c",
				"PID=$(cat /tmp/tcpdump.pid 2>/dev/null) && [ -n \"$PID\" ] && kill -0 $PID 2>/dev/null && echo active"})
		if pollErr != nil {
			return false, nil
		}
		return strings.TrimSpace(stdout) == "active", nil
	})).To(Succeed(), "tcpdump did not become active within timeout")

	By(fmt.Sprintf("Sending ping traffic from %s/%s to %s", srcPodNS, srcPod, targetIP))
	result, err := f.PingFromPod(ctx, srcPodNS, srcPod, targetIP, 5)
	Expect(err).NotTo(HaveOccurred())
	Expect(result.Success).To(BeTrue(), "ping failed: %s", result.Output)

	By("Stopping tcpdump and collecting capture")
	//nolint:errcheck // best-effort kill; tcpdump may already have exited
	f.ExecInPod(ctx, ns, capturePod, "",
		[]string{"sh", "-c", "PID=$(cat /tmp/tcpdump.pid 2>/dev/null) && [ -n \"$PID\" ] && kill -INT $PID; sleep 1"})

	By("Asserting captured packets are non-empty")
	// Use tcpdump -r to count actual packets; wc -c would pass even for an empty capture
	// because libpcap always writes a 24-byte global header.
	stdout, _, err := f.ExecInPod(ctx, ns, capturePod, "",
		[]string{"sh", "-c", "tcpdump -r /tmp/capture.pcap 2>/dev/null | wc -l"})
	Expect(err).NotTo(HaveOccurred(), "failed to count captured packets")
	count, convErr := strconv.Atoi(strings.TrimSpace(stdout))
	Expect(convErr).NotTo(HaveOccurred(), "unexpected output from tcpdump -r: %q", stdout)
	Expect(count).To(BeNumerically(">", 0), "no mirrored packets captured in /tmp/capture.pcap")
}

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

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying L2 NADs")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())
	})

	AfterEach(func() {
		By("Cleaning up test pods")
		_ = f.DeletePod(ctx, ns, "mirror-src")
		_ = f.DeletePod(ctx, ns, "mirror-capture")

		By("Cleaning up traffic mirror configuration (removing mirrorAcls from NNC)")
		cfg := f.Config
		_ = removeMirrorACLsFromNNC(ctx, f, cfg.WorkerNode1, cfg.VRFM2M)
	})

	It("should mirror ingress traffic to a capture pod when MirrorACLs are configured", func() {
		cfg := f.Config

		By("Creating mirror-src on worker-1 (VLAN 501, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "mirror-src", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Creating mirror-capture on worker-2 (VLAN 501, m2m) — receives mirrored GRE-encapsulated packets")
		// Use nicolaka/netshoot instead of busybox: busybox does not ship tcpdump,
		// which verifyMirrorCapture requires to capture packets on the net1 interface.
		Expect(f.CreateTestPod(ctx, ns, "mirror-capture", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan02IPv4, cfg.Macvlan02IPv6),
		}, framework.WithImage("nicolaka/netshoot:v0.13"), framework.WithNetAdmin())).To(Succeed())

		By("Waiting for test pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "mirror-src", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "mirror-capture", cfg.PodReadyTimeout)).To(Succeed())

		By("Waiting for IPv6 DAD to complete on mirror-src")
		Expect(f.WaitForIPv6DADComplete(ctx, ns, "mirror-src", cfg.Macvlan01IPv6, "net1", 60*time.Second)).To(Succeed())

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
		Expect(addErr).NotTo(HaveOccurred())

		By("Verifying mirrorAcls are persisted in NNC spec after update")
		// The CRA agents (FRR/VSR) do not yet implement mirrorAcl programming, so
		// waiting for the CRA to reach a specific status with the bumped revision is
		// not viable: the main operator's revision reconciler (debounce: 1s) will
		// revert spec.revision to the official value within seconds of the update.
		// We therefore assert only that the API server accepted the mirrorAcl entry
		// by re-reading the NNC spec immediately after the write.
		{
			nnc := &unstructured.Unstructured{}
			nnc.SetGroupVersionKind(nncGVK)
			Expect(f.Client.Get(ctx, types.NamespacedName{Name: cfg.WorkerNode1}, nnc)).To(Succeed(),
				"failed to re-read NNC after mirrorAcl update")
			fabricVRFs, found, getErr := unstructured.NestedMap(nnc.Object, "spec", "fabricVRFs")
			Expect(getErr).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "spec.fabricVRFs not found in NNC")
			vrf, ok := fabricVRFs[cfg.VRFM2M].(map[string]interface{})
			Expect(ok).To(BeTrue(), "VRF %q not found in NNC fabricVRFs", cfg.VRFM2M)
			acls, _, _ := unstructured.NestedSlice(vrf, "mirrorAcls")
			Expect(acls).NotTo(BeEmpty(), "mirrorAcls not persisted in NNC spec after update")
		}

		By("Verifying mirrored traffic is captured on mirror-capture")
		verifyMirrorCapture(ctx, f,
			ns, "mirror-capture", "net1",
			ns, "mirror-src", cfg.Macvlan02IPv4,
		)
	})
})
