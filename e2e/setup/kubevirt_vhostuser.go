package setup

import (
	"fmt"
	"strings"
	"time"
)

// PhaseKubeVirtVhostUser deploys the vhost-user VM datapath fixture on top of
// the KubeVirt install PhaseKubeVirt already did: our own device plugin, the
// onDefineDomain hook sidecar, a vhost-user NAD and a VM whose only data NIC is
// a DPDK vhost-user socket terminated by grout's net_vhost.
//
// It is a separate opt-in phase (E2E_KUBEVIRT_VHOSTUSER) rather than part of
// PhaseKubeVirt because it needs strictly more from the host than the routed VM
// fixture does: hugepages for the shared guest memory vhost-user maps, and a
// DPDK fast path to terminate the socket. It is grout-only for the second
// reason -- the cra-frr flavour has no fast path at all, and the vSR flavour
// comes with 6WIND's own plugin.
//
// Note that it also switches cluster-1 to the intent reconciler, which replaces
// the legacy pipeline: a lab brought up with this phase can no longer run the
// legacy suites.
func PhaseKubeVirtVhostUser(cluster *Cluster, _ string) error {
	if !isGrout() {
		Logf("Phase KubeVirt vhost-user: skipped (requires E2E_CRA_FLAVOR=grout)")
		return nil
	}
	Logf("Phase KubeVirt vhost-user: deploying device plugin + vhost-user VM fixture...")

	cp := cluster.ControlPlane()
	kubeconfigPath := "/etc/kubernetes/admin.conf"
	kubectl := func(args ...string) error {
		full := append([]string{"kubectl", "--kubeconfig=" + kubeconfigPath}, args...)
		_, err := DockerExec(cp.Name, full...)
		return err
	}

	// Hugepages must exist on the node before the VM is scheduled, and kubelet
	// only reports the hugepages capacity it saw at start. Checking here turns
	// an unschedulable-VM timeout several minutes later into an immediate,
	// legible failure.
	if err := checkNodeHugepages(cluster); err != nil {
		return err
	}

	imgBase := EnvOr("IMG_BASE", "ghcr.io/telekom")
	dpImg := imgBase + "/das-schiff-nwop-vhostuser-device-plugin:latest"
	hookImg := imgBase + "/das-schiff-nwop-kubevirt-vhostuser-hook:latest"
	for _, img := range []string{dpImg, hookImg} {
		Logf("Loading %s into nodes...", img)
		for _, node := range cluster.Nodes {
			if err := importImage(node.Name, img); err != nil {
				return fmt.Errorf("loading %s on %s: %w", img, node.Name, err)
			}
		}
	}

	Logf("Installing vhost-user device plugin DaemonSet...")
	if err := kubectl("apply", "-k", "/repo/e2e/kubevirt/deviceplugin"); err != nil {
		return fmt.Errorf("apply device plugin: %w", err)
	}
	if err := kubectl("-n", "kube-system", "rollout", "status",
		"daemonset/vhostuser-device-plugin", "--timeout=180s"); err != nil {
		return fmt.Errorf("device plugin rollout: %w", err)
	}

	// The plugin advertises its resources only after kubelet has accepted the
	// registration, and the VM is unschedulable until then.
	if err := waitForResource(cp.Name, kubeconfigPath,
		"network.t-caas.telekom.com/virtio-user"); err != nil {
		return err
	}

	// Registering the binding plugin is what makes a VM able to reference it,
	// and it carries two decisions:
	//
	//   - no domainAttachmentType: KubeVirt then does no pod-link discovery
	//     (which a vhost-user socket, having no kernel netdev, can never
	//     satisfy) and generates no <interface> element, leaving the whole
	//     device to the sidecar.
	//   - downwardAPI: device-info: KubeVirt projects the socket the device
	//     plugin allocated, as Multus reported it, into the sidecar. That is
	//     how the hook learns the path, so nothing has to be restated on the VM.
	//
	// Only the NetworkBindingPlugins gate is needed: the Sidecar gate guards
	// the hookSidecars annotation, which a registered sidecarImage does not use.
	if err := kubectl("-n", "kubevirt", "patch", "kubevirt", "kubevirt", "--type=merge",
		`-p={"spec":{"configuration":{"developerConfiguration":{"featureGates":`+
			`["HostDevices","NetworkBindingPlugins"]},`+
			`"network":{"binding":{"vhostuser":{"sidecarImage":"`+hookImg+`",`+
			`"downwardAPI":"device-info"}}}}}}`); err != nil {
		return fmt.Errorf("registering the vhostuser network binding plugin: %w", err)
	}

	// The VM attaches at layer 2, and an L2 attach binds to the NNC Layer2
	// whose stamped attachmentRef matches the NAD's layer2AttachmentRef. Only
	// the intent builder stamps that ref, so this fixture needs cluster-1 in
	// intent mode; the legacy Layer2NetworkConfiguration pipeline produces
	// Layer2s with no ref, which never match and are silently dropped.
	//
	// (Routed mode would need no intent CRDs, but is not usable here: grout
	// rejects the shared on-link gateway 169.254.1.1/32 on a second port with
	// EADDRINUSE -- see pkg/cra-grout/README.md.)
	if err := enableIntentMode(cp.Name, kubeconfigPath); err != nil {
		return err
	}

	Logf("Applying vhost-user NAD + VirtualMachine...")
	if err := kubectl("apply", "-f",
		"/repo/e2e/kubevirt/manifests/networkattachmentdefinition-grout-vhostuser.yaml"); err != nil {
		return fmt.Errorf("apply vhost-user NAD: %w", err)
	}
	if err := kubectl("apply", "-f",
		"/repo/e2e/kubevirt/manifests/virtualmachine-grout-vhostuser.yaml"); err != nil {
		return fmt.Errorf("apply vhost-user VM: %w", err)
	}

	if err := waitForNamedVMIRunning(cp.Name, kubeconfigPath, "grout-vhostuser-vm"); err != nil {
		return err
	}

	Logf("KubeVirt vhost-user VM fixture ready.")
	return nil
}

// intentRBACName is the ClusterRole/Binding granting the operator access to the
// intent CRD API group, matching the name the e2e framework uses.
const intentRBACName = "network-operator-intent"

// l2AttachmentName is the Layer2Attachment in the intent base configs that the
// vhost-user NAD attaches to (VLAN 501, VRF m2m).
const l2AttachmentName = "l2a-base-vlan501"

// hugepagesPerNode is the minimum number of 2MiB hugepages a node must have for
// a 1GiB guest plus grout's own DPDK mempools.
const hugepagesPerNode = 1024

// checkNodeHugepages fails early if the nodes cannot back a vhost-user VM.
func checkNodeHugepages(cluster *Cluster) error {
	for _, node := range cluster.Nodes {
		out, err := DockerExec(node.Name, "bash", "-c",
			"awk '/^HugePages_Total:/ {print $2}' /proc/meminfo")
		if err != nil {
			return fmt.Errorf("reading hugepages on %s: %w", node.Name, err)
		}
		var total int
		if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &total); err != nil {
			return fmt.Errorf("parsing hugepages on %s from %q: %w", node.Name, out, err)
		}
		if total < hugepagesPerNode {
			return fmt.Errorf("node %s has %d 2MiB hugepages, need at least %d; "+
				"set vm.nr_hugepages on the DOCKER HOST (kind nodes share its hugetlb pool)",
				node.Name, total, hugepagesPerNode)
		}
	}
	return nil
}

// waitForResource waits until at least one node advertises a device-plugin
// resource with a non-zero allocatable count.
func waitForResource(cpName, kubeconfigPath, resource string) error {
	Logf("Waiting for a node to advertise %s...", resource)
	return WaitFor("device plugin resource "+resource, 180*time.Second, 5*time.Second, func() (bool, error) {
		out, _ := DockerExec(cpName, "kubectl", "--kubeconfig="+kubeconfigPath,
			"get", "nodes", "-o",
			`jsonpath={range .items[*]}{.status.allocatable.`+
				strings.ReplaceAll(resource, ".", `\.`)+"}{'\\n'}{end}")
		for _, line := range strings.Fields(out) {
			if line != "" && line != "0" {
				return true, nil
			}
		}
		return false, nil
	})
}

// waitForNamedVMIRunning is waitForVMIRunning for an arbitrary VMI name. The
// vhost-user VM has more ways to hang than the routed one -- an unschedulable
// device request, a hook sidecar that rejects the domain -- so the wait reports
// what it last saw rather than just timing out.
func waitForNamedVMIRunning(cpName, kubeconfigPath, name string) error {
	// DockerExec merges stderr into its output, so a kubectl that failed would
	// otherwise be reported as the value that was asked for -- and indexing an
	// empty list is exactly such a failure, which is what the launcher query
	// below does whenever no pod exists yet. Keep the error and report nothing
	// rather than kubectl's complaint.
	kget := func(args ...string) string {
		full := append([]string{"kubectl", "--kubeconfig=" + kubeconfigPath, "-n", "default"}, args...)
		out, err := DockerExec(cpName, full...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}

	Logf("Waiting for %s VMI to be Running...", name)
	var lastPhase, lastReason string
	err := WaitFor(name+" running", vmiStartTimeout, vmiPollInterval, func() (bool, error) {
		lastPhase = kget("get", "vmi", name, "-o", "jsonpath={.status.phase}")
		if lastPhase == "Running" {
			return true, nil
		}
		lastReason = kget("get", "vmi", name, "-o",
			"jsonpath={.status.conditions[?(@.type=='Synchronized')].message}")
		if lastReason == "" {
			lastReason = kget("get", "pods", "-l", "kubevirt.io/vm="+name,
				"-o", "jsonpath={.items[*].status.conditions[?(@.type=='PodScheduled')].message}")
		}
		Logf("  %s: phase %q %s", name, lastPhase, lastReason)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("%s VMI not Running (last phase %q, %s): %w", name, lastPhase, lastReason, err)
	}
	return nil
}

// enableIntentMode switches cluster-1's operator from the legacy
// Layer2NetworkConfiguration/VRFRouteConfiguration pipeline to the intent
// reconciler and applies the intent base configs, so the node's Layer2s carry
// the attachmentRef an L2 workload attach binds to.
//
// The two pipelines are mutually exclusive (--enable-intent-reconciler disables
// the legacy ConfigReconciler), so the legacy CRs are removed first: leaving
// them would only strand objects nothing reconciles any more.
//
// This mirrors e2etests/framework.EnableIntentReconciler, which does the same
// from inside the suite; it is repeated here because the fixture has to exist
// before any test runs.
func enableIntentMode(cpName, kubeconfigPath string) error {
	Logf("Switching cluster-1 to the intent reconciler (needed for L2 attach)...")
	kubectl := func(args ...string) error {
		full := append([]string{"kubectl", "--kubeconfig=" + kubeconfigPath}, args...)
		_, err := DockerExec(cpName, full...)
		return err
	}

	// The intent CRDs are a different API group than the operator's own RBAC
	// covers, so without this the reconciler cannot even list them.
	if err := kubectl("create", "clusterrole", intentRBACName,
		"--verb=get,list,watch,create,update,patch,delete",
		"--resource=*.network-connector.sylvaproject.org"); err != nil {
		Logf("  clusterrole %s already exists, continuing", intentRBACName)
	}
	if err := kubectl("create", "clusterrolebinding", intentRBACName,
		"--clusterrole="+intentRBACName,
		"--serviceaccount=kube-system:network-operator-controller-manager"); err != nil {
		Logf("  clusterrolebinding %s already exists, continuing", intentRBACName)
	}

	for _, kind := range []string{"vrfrouteconfiguration", "layer2networkconfiguration"} {
		if err := kubectl("delete", kind, "--all", "--ignore-not-found"); err != nil {
			return fmt.Errorf("deleting legacy %s: %w", kind, err)
		}
	}

	if _, err := DockerExecShell(cpName, fmt.Sprintf(
		`kubectl --kubeconfig=%s -n kube-system patch deployment network-operator-operator --type=json `+
			`-p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--enable-intent-reconciler"},`+
			`{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--intent-namespace="}]'`,
		kubeconfigPath)); err != nil {
		return fmt.Errorf("patching operator for intent: %w", err)
	}
	if err := kubectl("-n", "kube-system", "rollout", "status",
		"deployment/network-operator-operator", "--timeout=180s"); err != nil {
		return fmt.Errorf("operator rollout after intent patch: %w", err)
	}

	// Retried: the intent validating webhooks are only registered by an
	// intent-enabled operator, and their certificates are generated a moment
	// after the pod reports ready, so the first apply usually races them.
	Logf("Applying intent base configs...")
	if err := WaitFor("intent base configs", 180*time.Second, 5*time.Second, func() (bool, error) {
		_, err := DockerExecShell(cpName, fmt.Sprintf(
			"kubectl --kubeconfig=%s apply -f /repo/e2etests/testdata/intent/base-configs.yaml",
			kubeconfigPath))
		return err == nil, nil
	}); err != nil {
		return fmt.Errorf("applying intent base configs: %w", err)
	}

	// The attach cannot bind before the Layer2 carrying the ref reaches the
	// node, and a VM started too early just sits with an unbound port.
	Logf("Waiting for the Layer2 attachmentRef to reach the NodeNetworkConfigs...")
	return WaitFor("layer2 attachmentRef in NNC", 5*time.Minute, 5*time.Second, func() (bool, error) {
		out, _ := DockerExec(cpName, "kubectl", "--kubeconfig="+kubeconfigPath,
			"get", "nodenetworkconfig", "-o",
			"jsonpath={.items[*].spec.layer2s.501.attachmentRef.name}")
		return strings.Contains(out, l2AttachmentName), nil
	})
}
