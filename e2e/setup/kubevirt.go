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

// vmiStartRestarts bounds how often a wedged VM start is restarted before the
// phase gives up. A healthy VMI reaches Running in one to two minutes, and a
// launcher that has reached a terminal phase is never going to bring the VMI
// up, so the restarts are only ever spent on that -- two of them, because a
// wedged launcher is detected in seconds and the overall deadline below is what
// really bounds the wait.
//
// vmiStartTimeout is the budget for the whole wait rather than per attempt. The
// guest boots under software emulation on a machine that is also running three
// CRAs and a two-cluster lab, and a per-attempt slice ends up throwing away a
// VMI that was merely slow: one had just got its containers started when its
// five-minute slice ran out, and its replacement then had to start from
// nothing. A restart only helps a launcher that is never going to come up, so
// restart on exactly that and let a slow one keep going. The trade-off is that
// a VMI stuck in Pending *without* a terminal launcher now burns the whole
// budget instead of being kicked halfway through; that kick never once helped,
// and the step it runs in allows 60 minutes.
const (
	vmiStartRestarts = 2
	vmiStartTimeout  = 12 * time.Minute
	vmiPollInterval  = 10 * time.Second
	// Every kubectl call is bounded. Nothing here runs under a context, so an
	// API server that stops answering would otherwise park the phase in a
	// single `kubectl` for as long as the job is allowed to run, and the
	// deadline below -- which is only consulted between calls -- would never be
	// reached.
	vmiKubectlTimeout = 30 * time.Second
	vmiDeleteTimeout  = 2 * time.Minute
)

// vmLauncherSelector matches only the launcher pods of the routed VM.
// `kubevirt.io/vm` is the label the VM template sets (see
// e2e/kubevirt/manifests/virtualmachine.yaml); `kubevirt.io=virt-launcher` keeps
// out anything else that might carry it.
const vmLauncherSelector = "kubevirt.io/vm=routed-vm,kubevirt.io=virt-launcher"

// waitForVMIRunning waits for the routed-vm VMI to reach Running, restarting it
// if its launcher pod died before the domain was ever defined. It polls
// directly rather than via WaitFor because it must abort a wedged start early
// instead of burning the whole timeout.
func waitForVMIRunning(cpName, kubeconfigPath string) error {
	deadline := time.Now().Add(vmiStartTimeout)

	// A single API call is bounded by the per-call ceiling *and* by what is left
	// of the overall budget, so that a server which has stopped answering
	// cannot push the phase past its deadline by one full call per query. The
	// one-second floor keeps the argument valid once the budget is spent; the
	// loop returns before issuing another call anyway.
	kubectlTimeout := func() time.Duration {
		left := time.Until(deadline)
		if left >= vmiKubectlTimeout {
			return vmiKubectlTimeout
		}
		if left < time.Second {
			return time.Second
		}
		return left
	}
	// DockerExec folds stderr into its output, so a failed kubectl would hand
	// back its error text as if it were the queried value. Every caller here
	// compares that value against something meaningful, so the error is kept
	// separate and the reading is only trusted when the call actually
	// succeeded.
	kget := func(args ...string) (string, error) {
		full := append([]string{
			"kubectl", "--kubeconfig=" + kubeconfigPath, "-n", "default",
			"--request-timeout=" + kubectlTimeout().String(),
		}, args...)
		out, err := DockerExec(cpName, full...)
		return strings.TrimSpace(out), err
	}
	// Launchers are selected by the VM they belong to, not by the generic
	// `kubevirt.io=virt-launcher` role: the vhost-user job runs a second VM in
	// the same namespace, and indexing into every launcher in the cluster picks
	// an arbitrary one of them. Every matching pod's phase is reported rather
	// than the first, because a restart legitimately overlaps two of them.
	launcherPhases := func() string {
		out, err := kget("get", "pods", "-l", vmLauncherSelector,
			"-o", "jsonpath={.items[*].status.phase}")
		if err != nil {
			return ""
		}
		return out
	}
	// A launcher that reached Failed is never going to bring its VMI up. It is
	// looked up by name so the restart can wait for that exact pod to go away:
	// the VM has `runStrategy: Always`, so virt-controller may well have built
	// the replacement before the kubelet has finished collecting its
	// predecessor, and waiting for *no* launcher to exist would then reject a
	// perfectly healthy one.
	//
	// The name is matched with `.items[*]` rather than `.items[0]`, because
	// jsonpath treats indexing an empty list as an error -- and no launcher has
	// failed for as long as the VM is healthy, which is the overwhelmingly
	// common case. Any name is as good as any other: they are all failed, and
	// the wait below simply follows whichever one was reported.
	failedLauncher := func() string {
		out, err := kget("get", "pods", "-l", vmLauncherSelector,
			"--field-selector=status.phase=Failed",
			"-o", "jsonpath={.items[*].metadata.name}")
		if err != nil {
			return ""
		}
		if names := strings.Fields(out); len(names) > 0 {
			return names[0]
		}
		return ""
	}
	// A query that did not succeed says nothing about the pod, so it is not
	// taken as proof that it is gone; the wait around this is bounded anyway.
	podGone := func(name string) bool {
		out, err := kget("get", "pod", name, "--ignore-not-found",
			"-o", "jsonpath={.metadata.name}")
		return err == nil && out == ""
	}

	restarts := 0
	var lastPhase, launcher string

	// Neither the delete nor the wait that follows it may run past the overall
	// budget: a restart that begins with seconds left would otherwise add
	// minutes to a wait that is supposed to be bounded. Clamp each of them to
	// what is actually left, recomputed at the point of use.
	remaining := func() time.Duration {
		left := time.Until(deadline)
		if left > vmiDeleteTimeout {
			return vmiDeleteTimeout
		}
		return left
	}

	for {
		// Checked before any call rather than after, so an expired budget
		// cannot buy another round of queries.
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"routed-vm VMI not Running after %s (last phase %q, launchers %q, %d restarts)",
				vmiStartTimeout, lastPhase, launcher, restarts)
		}
		lastPhase, _ = kget("get", "vmi", "routed-vm", "-o", "jsonpath={.status.phase}")
		if lastPhase == "Running" {
			return nil
		}
		launcher = launcherPhases()

		// An absent launcher is the normal state right after a restart and must
		// not count as one, so only a pod that actually reached Failed does.
		if failed := failedLauncher(); failed != "" &&
			restarts < vmiStartRestarts && remaining() > 0 {
			restarts++
			Logf("  launcher %s is Failed while VMI is %q; restarting routed-vm (%d/%d)",
				failed, lastPhase, restarts, vmiStartRestarts)
			if _, err := DockerExec(cpName, "kubectl", "--kubeconfig="+kubeconfigPath,
				"-n", "default", "delete", "vmi", "routed-vm",
				"--ignore-not-found", "--wait=true",
				"--timeout="+remaining().String(),
				"--request-timeout="+remaining().String()); err != nil {
				return fmt.Errorf("restarting routed-vm after a wedged start: %w", err)
			}
			// `delete vmi --wait` returns once the VMI is gone, but its launcher
			// pod is removed by the garbage collector afterwards. Letting the
			// loop run straight on would find that same dead pod and spend the
			// remaining restarts on a VMI that no longer exists.
			if left := remaining(); left > 0 {
				if err := WaitFor("launcher "+failed+" to be collected",
					left, vmiPollInterval, func() (bool, error) {
						return podGone(failed), nil
					}); err != nil {
					return fmt.Errorf("restarting routed-vm: %w", err)
				}
			}
			continue
		}

		Logf("  waiting for routed-vm VMI... (phase %q, launchers %q)", lastPhase, launcher)
		time.Sleep(vmiPollInterval)
	}
}
