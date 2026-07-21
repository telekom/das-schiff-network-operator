package setup

import (
	"fmt"
	"strings"
	"time"
)

// PhaseKubeVirt installs KubeVirt and deploys the routed-VM datapath fixture:
// the cni-routed installer DaemonSet, the routed NetworkAttachmentDefinition and
// a test VirtualMachine whose only data NIC is the routed secondary network.
//
// It is opt-in (invoked only when E2E_KUBEVIRT is set) because the base lab does
// not otherwise need KubeVirt. Teardown is handled by the normal `down` flow
// (the whole cluster is destroyed).
func PhaseKubeVirt(cluster *Cluster, repoRoot string) error {
	Logf("Phase KubeVirt: installing KubeVirt + routed VM fixture...")

	cp := cluster.ControlPlane()
	kubeconfigPath := "/etc/kubernetes/admin.conf"
	kubectl := func(args ...string) error {
		fullArgs := append([]string{"--kubeconfig=" + kubeconfigPath}, args...)
		_, err := DockerExec(cp.Name, append([]string{"kubectl"}, fullArgs...)...)
		return err
	}

	// Load the cni-routed image into every node's containerd.
	imgBase := EnvOr("IMG_BASE", "ghcr.io/telekom")
	cniImg := imgBase + "/das-schiff-cni-routed:latest"
	Logf("Loading %s into nodes...", cniImg)
	for _, node := range cluster.Nodes {
		if err := importImage(node.Name, cniImg); err != nil {
			return fmt.Errorf("loading %s on %s: %w", cniImg, node.Name, err)
		}
	}

	// Install the cni-routed plugin binary on all nodes.
	Logf("Installing cni-routed installer DaemonSet...")
	if err := kubectl("apply", "-f", "/repo/e2e/kubevirt/install/daemonset.yaml"); err != nil {
		return fmt.Errorf("apply cni-routed installer: %w", err)
	}

	// Install KubeVirt operator + CR.
	kvVersion := EnvOr("KUBEVIRT_VERSION", "v1.4.0")
	Logf("Installing KubeVirt %s...", kvVersion)
	base := fmt.Sprintf("https://github.com/kubevirt/kubevirt/releases/download/%s", kvVersion)
	if err := kubectl("apply", "-f", base+"/kubevirt-operator.yaml"); err != nil {
		return fmt.Errorf("apply kubevirt operator: %w", err)
	}
	if err := kubectl("apply", "-f", base+"/kubevirt-cr.yaml"); err != nil {
		return fmt.Errorf("apply kubevirt cr: %w", err)
	}

	// Enable software emulation (nested virt / emulation is enough for a tiny VM
	// that just needs an IP) and wait for KubeVirt to converge.
	kubectl("-n", "kubevirt", "patch", "kubevirt", "kubevirt", "--type=merge", //nolint:errcheck
		`-p={"spec":{"configuration":{"developerConfiguration":{"useEmulation":true}}}}`)

	Logf("Waiting for KubeVirt to be Deployed...")
	if err := WaitFor("kubevirt deployed", 300*time.Second, 10*time.Second, func() (bool, error) {
		out, _ := DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath,
			"-n", "kubevirt", "get", "kubevirt", "kubevirt",
			"-o", "jsonpath={.status.phase}")
		return strings.TrimSpace(out) == "Deployed", nil
	}); err != nil {
		return fmt.Errorf("kubevirt not ready: %w", err)
	}

	// Deploy the routed NAD + test VM.
	Logf("Applying routed NAD + VirtualMachine...")
	if err := kubectl("apply", "-f", "/repo/e2e/kubevirt/manifests/networkattachmentdefinition.yaml"); err != nil {
		return fmt.Errorf("apply NAD: %w", err)
	}
	if err := kubectl("apply", "-f", "/repo/e2e/kubevirt/manifests/virtualmachine.yaml"); err != nil {
		return fmt.Errorf("apply VM: %w", err)
	}

	// Wait for the VMI to be Running.
	Logf("Waiting for routed-vm VMI to be Running...")
	if err := WaitFor("vmi running", 300*time.Second, 10*time.Second, func() (bool, error) {
		out, _ := DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath,
			"-n", "default", "get", "vmi", "routed-vm",
			"-o", "jsonpath={.status.phase}")
		return strings.TrimSpace(out) == "Running", nil
	}); err != nil {
		return fmt.Errorf("routed-vm VMI not Running: %w", err)
	}

	Logf("KubeVirt routed VM fixture ready.")
	return nil
}
