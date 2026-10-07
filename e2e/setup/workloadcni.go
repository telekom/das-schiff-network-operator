package setup

import (
	"fmt"
	"strings"
	"time"
)

// workloadCNIInstallerManifest installs the cni-workload plugin binary on every
// node. Only the binary is shipped; the per-network CNI config is delivered as a
// NetworkAttachmentDefinition through Multus.
const workloadCNIInstallerManifest = "/repo/e2e/kubevirt/install/daemonset.yaml"

// workloadCNIInstallTimeout bounds the wait for the installer DaemonSet to have
// dropped the plugin binary on every node.
const workloadCNIInstallTimeout = 180 * time.Second

// PhaseWorkloadCNI makes the workload CNI plugin usable in the lab: it imports
// the plugin image into every node's containerd and rolls out the installer
// DaemonSet that drops the binary into /opt/cni/bin.
//
// This runs in every lab, not only the KubeVirt one — the intent suite attaches
// plain pods through the plugin (L2 attach, trunk, routed IPAM), which needs
// nothing beyond the binary and the agent-side gRPC socket the CRA agents
// already serve.
func PhaseWorkloadCNI(cluster *Cluster) error {
	Logf("Phase workload CNI: installing the cni-workload plugin...")

	imgBase := EnvOr("IMG_BASE", "ghcr.io/telekom")
	cniImg := imgBase + "/das-schiff-nwop-cni-workload:latest"
	Logf("Loading %s into nodes...", cniImg)
	for _, node := range cluster.Nodes {
		if err := importImage(node.Name, cniImg); err != nil {
			return fmt.Errorf("loading %s on %s: %w", cniImg, node.Name, err)
		}
	}

	cp := cluster.ControlPlane()
	Logf("Applying cni-workload installer DaemonSet...")
	if _, err := DockerExec(cp.Name, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf",
		"apply", "-f", workloadCNIInstallerManifest); err != nil {
		return fmt.Errorf("apply cni-workload installer: %w", err)
	}

	// The manifest hardcodes the published image. Point the DaemonSet at the
	// image that was actually imported, or a lab built with a custom IMG_BASE
	// pulls (or fails to pull) someone else's binary.
	if _, err := DockerExec(cp.Name, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf",
		"-n", "kube-system", "set", "image", "daemonset/cni-workload-installer",
		"installer="+cniImg); err != nil {
		return fmt.Errorf("pin cni-workload installer image to %s: %w", cniImg, err)
	}

	// Wait for the binary to actually be on disk everywhere: a pod scheduled
	// before the installer has run fails CNI ADD with "plugin not found", which
	// is a confusing way for a datapath test to fail.
	if err := WaitFor("cni-workload installer ready", workloadCNIInstallTimeout, 5*time.Second, func() (bool, error) {
		out, _ := DockerExec(cp.Name, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf",
			"-n", "kube-system", "get", "daemonset", "cni-workload-installer",
			"-o", "jsonpath={.status.numberReady}/{.status.desiredNumberScheduled}")
		out = strings.TrimSpace(out)
		ready, desired, ok := strings.Cut(out, "/")
		return ok && desired != "" && desired != "0" && ready == desired, nil
	}); err != nil {
		return fmt.Errorf("cni-workload installer not ready: %w", err)
	}

	Logf("Workload CNI plugin installed.")
	return nil
}
