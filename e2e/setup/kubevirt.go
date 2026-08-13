package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// guestDiskImage is the container disk backing the routed test VM. It must stay
// in sync with the containerDisk image in
// e2e/kubevirt/manifests/virtualmachine.yaml.
const guestDiskImage = "quay.io/kubevirt/fedora-cloud-container-disk-demo:latest"

// preloadGuestImage pulls the guest container disk on the host once and imports
// it into every node's containerd. Without this, each node pulls the (large)
// disk itself while the VMI is starting, which dominates start-up time and is
// the main source of flakiness in the VMI-running wait.
func preloadGuestImage(cluster *Cluster) error {
	Logf("Preloading guest container disk %s...", guestDiskImage)
	if err := RunCmd("docker", "pull", guestDiskImage); err != nil {
		return fmt.Errorf("pulling guest disk %s: %w", guestDiskImage, err)
	}
	for _, node := range cluster.Nodes {
		if err := importImage(node.Name, guestDiskImage); err != nil {
			return fmt.Errorf("loading %s on %s: %w", guestDiskImage, node.Name, err)
		}
	}
	return nil
}

// PhaseKubeVirt installs KubeVirt and deploys the routed-VM datapath fixture:
// the routed NetworkAttachmentDefinition and a test VirtualMachine whose only
// data NIC is the routed secondary network. The cni-workload plugin it relies on
// is installed by PhaseWorkloadCNI, which runs for every lab.
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
	//
	// KubeVirt sizes the compute container's memory limit from the guest's
	// memory plus a fixed overhead that assumes KVM. QEMU under TCG needs
	// considerably more than that -- a hugepage-backed guest gets OOMKilled
	// mid-boot -- so the overhead is doubled here. This is only needed because
	// CI runners have no nested virtualisation.
	kubectl("-n", "kubevirt", "patch", "kubevirt", "kubevirt", "--type=merge", //nolint:errcheck
		`-p={"spec":{"configuration":{"developerConfiguration":{"useEmulation":true},`+
			`"additionalGuestMemoryOverheadRatio":"2.0"}}}`)

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
	// Preload the guest container disk so VM startup does not race a large
	// in-node registry pull, which is the slowest and least predictable step.
	if err := preloadGuestImage(cluster); err != nil {
		return err
	}
	Logf("Applying routed NAD + VirtualMachine...")
	nadManifest, err := os.ReadFile(filepath.Join(repoRoot, "e2e", "kubevirt", "manifests", "networkattachmentdefinition.yaml"))
	if err != nil {
		return fmt.Errorf("reading routed NAD: %w", err)
	}
	if err := DockerExecInput(cp.Name, renderWorkloadNAD(string(nadManifest)),
		"kubectl", "--kubeconfig="+kubeconfigPath, "apply", "-f", "-"); err != nil {
		return fmt.Errorf("apply NAD: %w", err)
	}
	if err := kubectl("apply", "-f", "/repo/e2e/kubevirt/manifests/virtualmachine.yaml"); err != nil {
		return fmt.Errorf("apply VM: %w", err)
	}

	// Wait for the VMI to be Running. The guest boots under software emulation,
	// which is slow and racy: the launcher can time out waiting for libvirt to
	// define the domain while the VMI is still Pending, and then never recovers
	// on its own. Detect that dead end and restart the VMI instead of failing
	// the whole lab. The phase is reported on give-up to keep CI diagnosable.
	if err := waitForVMIRunning(cp.Name, kubeconfigPath); err != nil {
		return err
	}

	Logf("KubeVirt routed VM fixture ready.")
	return nil
}

// vmiStartAttempts bounds how often a wedged VM start is retried before the
// phase gives up. A healthy VMI reaches Running in one to two minutes, and the
// wait already aborts early once the launcher pod reaches a terminal phase, so
// the full budget is only ever burnt by a VMI that virt-controller never
// advances -- a state that does not recover on its own. Keeping the ceiling at
// two short attempts leaves the job enough time to collect debug artifacts.
const (
	vmiStartAttempts = 2
	vmiStartTimeout  = 5 * time.Minute
	vmiPollInterval  = 10 * time.Second
)

// waitForVMIRunning waits for the routed-vm VMI to reach Running, restarting it
// if its launcher pod died before the domain was ever defined. It polls
// directly rather than via WaitFor because it must abort a wedged attempt
// early instead of burning the whole timeout.
func waitForVMIRunning(cpName, kubeconfigPath string) error {
	kget := func(args ...string) string {
		full := append([]string{"kubectl", "--kubeconfig=" + kubeconfigPath, "-n", "default"}, args...)
		out, _ := DockerExec(cpName, full...)
		return strings.TrimSpace(out)
	}

	var lastPhase string
	for attempt := 1; attempt <= vmiStartAttempts; attempt++ {
		Logf("Waiting for routed-vm VMI to be Running (attempt %d/%d)...", attempt, vmiStartAttempts)

		deadline := time.Now().Add(vmiStartTimeout)
		wedged := false
		for {
			lastPhase = kget("get", "vmi", "routed-vm", "-o", "jsonpath={.status.phase}")
			if lastPhase == "Running" {
				return nil
			}
			// A launcher that reached a terminal phase can never bring the VMI
			// up; retrying immediately is much cheaper than waiting it out.
			launcher := kget("get", "pods", "-l", "kubevirt.io=virt-launcher",
				"-o", "jsonpath={.items[0].status.phase}")
			if launcher == "Failed" || launcher == "Succeeded" {
				wedged = true
				Logf("  launcher pod is %s while VMI is %q", launcher, lastPhase)
				break
			}
			if time.Now().After(deadline) {
				break
			}
			Logf("  waiting for routed-vm VMI... (phase %q, launcher %q)", lastPhase, launcher)
			time.Sleep(vmiPollInterval)
		}

		if attempt == vmiStartAttempts {
			return fmt.Errorf("routed-vm VMI not Running after %d attempts (last phase %q, wedged=%t)",
				vmiStartAttempts, lastPhase, wedged)
		}
		Logf("VM start did not complete (phase %q); restarting routed-vm...", lastPhase)
		if _, err := DockerExec(cpName, "kubectl", "--kubeconfig="+kubeconfigPath,
			"-n", "default", "delete", "vmi", "routed-vm",
			"--ignore-not-found", "--wait=true"); err != nil {
			return fmt.Errorf("restarting routed-vm after a wedged start: %w", err)
		}
	}
	return nil
}
