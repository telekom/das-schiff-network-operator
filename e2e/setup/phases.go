package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PhaseBuildImages builds all Docker images required for the E2E lab.
func PhaseBuildImages(repoRoot string) error {
	Logf("Building images...")

	imgBase := EnvOr("IMG_BASE", "ghcr.io/telekom")
	kindNodeVersion := EnvOr("KIND_NODE_VERSION", "v1.32.2")
	nodeImage := EnvOr("E2E_NODE_IMAGE", imgBase+"/das-schiff-kind-node:"+kindNodeVersion)
	nat64Image := EnvOr("E2E_NAT64_IMAGE", imgBase+"/das-schiff-nat64:latest")
	testerImage := EnvOr("E2E_TESTER_IMAGE", imgBase+"/das-schiff-e2e-tester:latest")

	ldflags := getLDFlags(repoRoot)

	// 1. Build cra-frr image
	Logf("  Building das-schiff-cra-frr...")
	if err := RunCmd("docker", "build",
		"--build-arg", "ldflags="+ldflags,
		"-f", filepath.Join(repoRoot, "das-schiff-cra-frr.Dockerfile"),
		"-t", "das-schiff-cra-frr:latest",
		repoRoot,
	); err != nil {
		return fmt.Errorf("building cra-frr: %w", err)
	}

	// 2. Export cra-frr and build kind node image (with cra-frr baked in)
	craFRRTar := filepath.Join(repoRoot, "e2e", "images", "kind-node", "cra-frr.tar")
	Logf("  Exporting cra-frr tar...")
	if err := RunCmd("docker", "save", "-o", craFRRTar, "das-schiff-cra-frr:latest"); err != nil {
		return fmt.Errorf("saving cra-frr tar: %w", err)
	}

	Logf("  Building kind node image (%s)...", nodeImage)
	kindNodeCtx := filepath.Join(repoRoot, "e2e", "images", "kind-node")
	if err := RunCmd("docker", "build",
		"--build-arg", "KIND_NODE_VERSION="+kindNodeVersion,
		"-t", nodeImage,
		"-f", filepath.Join(kindNodeCtx, "Dockerfile"),
		kindNodeCtx,
	); err != nil {
		os.Remove(craFRRTar)
		return fmt.Errorf("building kind-node: %w", err)
	}
	os.Remove(craFRRTar)

	// 3. Build operator + agent + platform images
	Logf("  Building operator + agent + platform images...")
	if err := RunCmd("docker", "build",
		"--build-arg", "ldflags="+ldflags,
		"-f", filepath.Join(repoRoot, "das-schiff-network-operator.Dockerfile"),
		"-t", imgBase+"/das-schiff-network-operator:latest",
		repoRoot,
	); err != nil {
		return fmt.Errorf("building operator: %w", err)
	}

	if err := RunCmd("docker", "build",
		"--build-arg", "ldflags="+ldflags,
		"-f", filepath.Join(repoRoot, "das-schiff-nwop-agent-cra-frr.Dockerfile"),
		"-t", imgBase+"/das-schiff-nwop-agent-cra-frr:latest",
		repoRoot,
	); err != nil {
		return fmt.Errorf("building agent-cra-frr: %w", err)
	}

	if err := RunCmd("docker", "build",
		"--build-arg", "ldflags="+ldflags,
		"-f", filepath.Join(repoRoot, "das-schiff-nwop-agent-netplan.Dockerfile"),
		"-t", imgBase+"/das-schiff-nwop-agent-netplan:latest",
		repoRoot,
	); err != nil {
		return fmt.Errorf("building agent-netplan: %w", err)
	}

	if err := RunCmd("docker", "build",
		"--build-arg", "ldflags="+ldflags,
		"-f", filepath.Join(repoRoot, "das-schiff-platform-coil.Dockerfile"),
		"-t", imgBase+"/das-schiff-platform-coil:latest",
		repoRoot,
	); err != nil {
		return fmt.Errorf("building platform-coil: %w", err)
	}

	if err := RunCmd("docker", "build",
		"--build-arg", "ldflags="+ldflags,
		"-f", filepath.Join(repoRoot, "das-schiff-platform-metallb.Dockerfile"),
		"-t", imgBase+"/das-schiff-platform-metallb:latest",
		repoRoot,
	); err != nil {
		return fmt.Errorf("building platform-metallb: %w", err)
	}

	if err := RunCmd("docker", "build",
		"--build-arg", "ldflags="+ldflags,
		"-f", filepath.Join(repoRoot, "das-schiff-network-sync.Dockerfile"),
		"-t", imgBase+"/das-schiff-network-sync:latest",
		repoRoot,
	); err != nil {
		return fmt.Errorf("building network-sync: %w", err)
	}

	// 4. Build NAT64 image
	nat64Ctx := filepath.Join(repoRoot, "e2e", "images", "nat64")
	Logf("  Building NAT64 image (%s)...", nat64Image)
	if err := RunCmd("docker", "build",
		"-t", nat64Image,
		"-f", filepath.Join(nat64Ctx, "Dockerfile"),
		nat64Ctx,
	); err != nil {
		return fmt.Errorf("building nat64: %w", err)
	}

	// 5. Build tester image
	testerCtx := filepath.Join(repoRoot, "e2e", "images", "tester")
	Logf("  Building tester image (%s)...", testerImage)
	if err := RunCmd("docker", "build",
		"-t", testerImage,
		"-f", filepath.Join(testerCtx, "Dockerfile"),
		testerCtx,
	); err != nil {
		return fmt.Errorf("building tester: %w", err)
	}

	Logf("All images built successfully.")
	return nil
}

// getLDFlags runs hack/version.sh and returns the ldflags string.
func getLDFlags(repoRoot string) string {
	cmd := exec.Command("bash", filepath.Join(repoRoot, "hack", "version.sh"))
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Phase 1a: Create k8s node containers with proper Docker flags.
// Containerlab's kind:linux can't provide --volume /var (anonymous volume) needed
// to avoid nested overlay errors. We create the containers ourselves, then
// containerlab wires them using ext-container.
func PhaseCreateNodes(cluster *Cluster, repoRoot string) error {
	Logf("Phase 1a: Creating k8s node containers...")

	nodeImage := EnvOr("E2E_NODE_IMAGE", "ghcr.io/telekom/das-schiff-kind-node:v1.32.2")
	genDir := filepath.Join(repoRoot, "e2e", "generated")

	for _, node := range cluster.Nodes {
		shortName := node.Name // e.g. "nwop-control-plane"

		// Clean up any stale container or network endpoint from a previous run
		RunCmd("docker", "rm", "-f", shortName)                            //nolint:errcheck
		RunCmd("docker", "network", "disconnect", "-f", "none", shortName) //nolint:errcheck

		Logf("Creating container %s...", shortName)

		args := []string{
			"create",
			"--name", shortName,
			"--hostname", shortName,
			"--privileged",
			"--security-opt", "seccomp=unconfined",
			"--security-opt", "apparmor=unconfined",
			"--tmpfs", "/tmp",
			"--tmpfs", "/run",
			"--volume", "/var",
			"--network", "none",
			"--restart", "on-failure:1",
			"-e", "container=docker",
			"-v", filepath.Join(genDir, shortName, "cra") + ":/etc/cra",
			"-v", filepath.Join(genDir, shortName, "node-identity.env") + ":/etc/node-identity.env:ro",
			"-v", filepath.Join(genDir, shortName, "netplan") + ":/etc/netplan",
			"-v", filepath.Join(genDir, shortName, "systemd-network/10-netplan-hbn.network.d") + ":/etc/systemd/network/10-netplan-hbn.network.d:ro",
			"-v", repoRoot + ":/repo:ro",
			// Mount /lib/modules from the VM kernel (Docker Desktop/OrbStack maps this
			// from the Linux VM, not macOS host). Needed for modprobe (e.g., fou module).
			"-v", "/lib/modules:/lib/modules:ro",
			nodeImage,
		}
		if err := RunCmd("docker", args...); err != nil {
			return fmt.Errorf("creating container %s: %w", shortName, err)
		}
		// Start the container so systemd boots
		if err := RunCmd("docker", "start", shortName); err != nil {
			return fmt.Errorf("starting container %s: %w", shortName, err)
		}
	}

	Logf("All k8s node containers created and running")
	return nil
}

// Phase 1b: Deploy containerlab topology.
// Creates fabric/infra containers and wires everything (k8s nodes are ext-container).
func PhaseContainerlab(repoRoot string) error {
	Logf("Phase 1b: Deploying containerlab topology...")

	topoDir := filepath.Join(repoRoot, "e2e", "setup")
	clabImage := EnvOr("CLAB_IMAGE", "ghcr.io/srl-labs/clab:0.74.0")

	args := []string{
		"run", "--rm",
		"--privileged",
		"--network", "host",
		"--pid", "host",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", topoDir + ":" + topoDir,
		"-v", repoRoot + ":" + repoRoot,
		"-w", topoDir,
	}
	// Forward E2E_* env vars so containerlab can resolve ${E2E_*} in the topology.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "E2E_") {
			args = append(args, "-e", kv)
		}
	}
	args = append(args, clabImage,
		"containerlab", "deploy", "--topo", "topology.clab.yml",
	)

	return RunCmd("docker", args...)
}

// Phase 2: Start CRA and wait for BGP convergence.
// After containerlab wires eth1/eth2, start CRA which creates hbn veth,
// starts FRR, and establishes BGP sessions with the leaf switches.
func PhaseUnderlay(cluster *Cluster) error {
	Logf("Phase 2: Configuring underlay...")

	for _, node := range cluster.Nodes {
		Logf("Starting CRA on %s...", node.Name)

		// CRA configs are already bind-mounted at /etc/cra.
		// systemd generator reads /etc/cra/interfaces + /etc/cra/flavour → creates cra.service.
		if _, err := DockerExec(node.Name, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("daemon-reload on %s: %w", node.Name, err)
		}
		if _, err := DockerExec(node.Name, "systemctl", "restart", "cra.service"); err != nil {
			return fmt.Errorf("starting CRA on %s: %w", node.Name, err)
		}

		// Add kubelet→CRA dependency so kubelet waits for CRA
		depUnit := "[Unit]\nAfter=cra.service\nWants=cra.service\n"
		if _, err := DockerExecShell(node.Name,
			fmt.Sprintf("printf '%s' > /etc/systemd/system/kubelet.service.d/10-cra-dependency.conf", depUnit),
		); err != nil {
			return fmt.Errorf("kubelet dependency on %s: %w", node.Name, err)
		}
		if _, err := DockerExec(node.Name, "systemctl", "daemon-reload"); err != nil {
			return err
		}

		// Wait for CRA to create the hbn veth pair
		if err := WaitFor(fmt.Sprintf("hbn on %s", node.Name), 60*time.Second, 2*time.Second, func() (bool, error) {
			_, err := DockerExec(node.Name, "ip", "link", "show", "hbn")
			return err == nil, nil
		}); err != nil {
			return fmt.Errorf("waiting for hbn on %s: %w", node.Name, err)
		}

		// Apply per-node netplan config (10-hbn.yaml mounted via Docker volume).
		// Configures addresses + IPv6 default route on the hbn veth.
		// IPv4 default via IPv6 next-hop is handled by a networkd drop-in
		// (cross-family / RFC 5549 — not expressible in netplan).
		if _, err := DockerExec(node.Name, "netplan", "apply"); err != nil {
			return fmt.Errorf("netplan apply on %s: %w", node.Name, err)
		}
	}

	// Wait for BGP convergence
	Logf("Waiting for BGP convergence...")
	return WaitFor("BGP convergence", 120*time.Second, 5*time.Second, func() (bool, error) {
		out, err := DockerExec("clab-nwop-leaf1", "vtysh", "-c", "show bgp summary json")
		if err != nil {
			return false, err
		}
		// vtysh may print warnings before the JSON (e.g. "% Can't open vtysh.conf")
		if idx := strings.Index(out, "{"); idx > 0 {
			out = out[idx:]
		}
		var summary map[string]interface{}
		if err := json.Unmarshal([]byte(out), &summary); err != nil {
			return false, err
		}
		ipv4, ok := summary["ipv4Unicast"].(map[string]interface{})
		if !ok {
			return false, nil
		}
		peers, ok := ipv4["peers"].(map[string]interface{})
		if !ok {
			return false, nil
		}
		established := 0
		for _, v := range peers {
			peer, ok := v.(map[string]interface{})
			if !ok {
				continue
			}
			if peer["state"] == "Established" {
				established++
			}
		}
		Logf("  BGP: %d/4 peers established on leaf1", established)
		return established >= 4, nil
	})
}

// Phase 3: Configure NAT64/DNS64 and deploy kube-vip.
// These run BEFORE kubeadm so DNS resolution and VIP are ready.
func PhasePreKubeadm(cluster *Cluster, repoRoot string) error {
	Logf("Phase 3: Pre-kubeadm setup (NAT64 + kube-vip)...")

	// Wait for NAT64 services
	Logf("Verifying NAT64 services...")
	if err := WaitFor("NAT64 container", 60*time.Second, 3*time.Second, func() (bool, error) {
		out, _ := DockerExecShell("clab-nwop-nat64", "pgrep tayga && pgrep unbound")
		return strings.TrimSpace(out) != "", nil
	}); err != nil {
		return err
	}
	Logf("NAT64 services running")

	// Wait for NAT64 route to propagate via EVPN
	Logf("Waiting for NAT64 route in cluster VRF...")
	if err := WaitFor("NAT64 route", 120*time.Second, 5*time.Second, func() (bool, error) {
		craPID, err := getCRAPID(cluster.Nodes[1].Name) // check on worker
		if err != nil {
			return false, err
		}
		out, _ := DockerExec(cluster.Nodes[1].Name,
			"nsenter", "-t", craPID, "-m", "-n", "--",
			"vtysh", "-c", fmt.Sprintf("show ipv6 route vrf cluster %s/128", cluster.NAT64DNS))
		return strings.Contains(out, cluster.NAT64DNS), nil
	}); err != nil {
		return err
	}

	// Configure DNS on all nodes → NAT64 unbound
	Logf("Configuring DNS → NAT64 (%s)...", cluster.NAT64DNS)
	for _, node := range cluster.Nodes {
		if _, err := DockerExecShell(node.Name,
			fmt.Sprintf("printf 'nameserver %s\\n' > /etc/resolv.conf", cluster.NAT64DNS),
		); err != nil {
			return fmt.Errorf("setting DNS on %s: %w", node.Name, err)
		}
	}

	// Deploy kube-vip static pod manifest on control-plane
	cp := cluster.ControlPlane()
	Logf("Deploying kube-vip static pod (VIP=%s)...", cluster.VIP)
	tplPath := filepath.Join(repoRoot, "e2e", "images", "kind-node", "kube-vip.yaml")
	tplContent, err := os.ReadFile(tplPath)
	if err != nil {
		return fmt.Errorf("reading kube-vip template: %w", err)
	}
	manifest := strings.ReplaceAll(string(tplContent), "__VIP_ADDRESS__", cluster.VIP)
	// kube-vip manifest goes to /etc/kube-vip/ for now;
	// it will be moved to /etc/kubernetes/manifests/ after kubeadm init creates that dir
	if err := DockerExecInput(cp.Name, manifest, "tee", "/etc/kube-vip/kube-vip.yaml"); err != nil {
		return fmt.Errorf("writing kube-vip manifest: %w", err)
	}

	// Disable rp_filter on CRA netns (needed for cross-VRF traffic)
	Logf("Disabling rp_filter on CRA network namespaces...")
	for _, node := range cluster.Nodes {
		craPID, err := getCRAPID(node.Name)
		if err != nil {
			Logf("  WARNING: couldn't get CRA PID on %s: %v", node.Name, err)
			continue
		}
		DockerExec(node.Name, "nsenter", "-t", craPID, "-n", "bash", "-c", //nolint:errcheck
			"for iface in all default br_cluster vx_cluster br_mgmt vx_mgmt hbn cluster mgmt; do echo 0 > /proc/sys/net/ipv4/conf/$iface/rp_filter 2>/dev/null; done")
		Logf("  %s: rp_filter disabled", node.Name)
	}

	return nil
}

// Phase 4: Run kubeadm init + join.
// The fabric is fully converged, DNS points to NAT64, kube-vip template is ready.
func PhaseKubeadm(cluster *Cluster) error {
	Logf("Phase 4: Running kubeadm...")

	cp := cluster.ControlPlane()

	// Add VIP to loopback so kubeadm can reach the API server at the
	// controlPlaneEndpoint address. The API server binds to :: (all interfaces),
	// so with the VIP on lo it becomes locally reachable.
	// kube-vip will take over VIP management after it starts.
	Logf("Adding VIP %s to control-plane loopback...", cluster.VIP)
	if _, err := DockerExec(cp.Name, "ip", "addr", "add", cluster.VIP+"/32", "dev", "lo"); err != nil {
		return fmt.Errorf("adding VIP to lo: %w", err)
	}

	// kubeadm init
	if err := KubeadmInit(cluster); err != nil {
		return err
	}

	// Place kube-vip manifest now that /etc/kubernetes/manifests/ exists
	Logf("Activating kube-vip static pod...")
	if _, err := DockerExec(cp.Name, "cp", "/etc/kube-vip/kube-vip.yaml", "/etc/kubernetes/manifests/kube-vip.yaml"); err != nil {
		return fmt.Errorf("activating kube-vip: %w", err)
	}

	// Wait for VIP to become reachable
	Logf("Waiting for kube-vip VIP %s...", cluster.VIP)
	if err := WaitFor("kube-vip VIP", 90*time.Second, 3*time.Second, func() (bool, error) {
		out, _ := DockerExecShell(cp.Name,
			fmt.Sprintf("curl -sk --noproxy '*' https://%s:6443/healthz 2>/dev/null", cluster.VIP))
		return strings.TrimSpace(out) == "ok", nil
	}); err != nil {
		return err
	}
	Logf("VIP is reachable")

	// Install kube-proxy
	if err := InstallKubeProxy(cluster); err != nil {
		return err
	}

	// Join workers
	for _, w := range cluster.Workers() {
		if err := KubeadmJoin(cluster, w); err != nil {
			return err
		}
	}

	return nil
}

// Phase 5: Install cluster components.
// Calico, Coil, Multus, MetalLB, CNI plugins, operator + agents.
func PhaseComponents(cluster *Cluster, repoRoot string) error {
	Logf("Phase 5: Installing cluster components...")

	cp := cluster.ControlPlane()

	// Set KUBECONFIG for kubectl commands
	kubeconfigPath := "/etc/kubernetes/admin.conf"

	// Helper for kubectl via control-plane
	kubectl := func(args ...string) error {
		fullArgs := append([]string{"--kubeconfig=" + kubeconfigPath}, args...)
		_, err := DockerExec(cp.Name, append([]string{"kubectl"}, fullArgs...)...)
		return err
	}

	// Load images into containerd on all nodes
	Logf("Loading operator/agent/platform images into containerd...")
	imgBase := EnvOr("IMG_BASE", "ghcr.io/telekom")
	images := []string{
		imgBase + "/das-schiff-network-operator:latest",
		imgBase + "/das-schiff-nwop-agent-cra-frr:latest",
		imgBase + "/das-schiff-nwop-agent-netplan:latest",
		imgBase + "/das-schiff-platform-coil:latest",
		imgBase + "/das-schiff-platform-metallb:latest",
		imgBase + "/das-schiff-network-sync:latest",
	}
	for _, node := range cluster.Nodes {
		for _, img := range images {
			// Import from host docker → containerd in node
			Logf("  Loading %s on %s", filepath.Base(img), node.Name)
			if err := importImage(node.Name, img); err != nil {
				return fmt.Errorf("loading image %s on %s: %w", img, node.Name, err)
			}
		}
	}

	// IPv6 MASQUERADE rules
	Logf("Adding IPv6 MASQUERADE rules...")
	for _, node := range cluster.Nodes {
		DockerExec(node.Name, "ip6tables", "-t", "nat", "-A", "POSTROUTING", //nolint:errcheck
			"-s", "fd10:244::/56", "!", "-d", "fd10:244::/56",
			"-j", "MASQUERADE", "--random-fully")
	}

	// Install CNI plugins
	Logf("Installing CNI plugins...")
	arch := runtime.GOARCH
	cniVersion := EnvOr("CNI_PLUGINS_VERSION", "v1.9.0")
	cniURL := fmt.Sprintf(
		"https://github.com/containernetworking/plugins/releases/download/%s/cni-plugins-linux-%s-%s.tgz",
		cniVersion, arch, cniVersion)
	for _, node := range cluster.Nodes {
		// Install into both /opt/cni/bin (kubelet default) and /usr/lib/cni (Debian default)
		// to ensure the correct version is used regardless of CNI path configuration.
		if _, err := DockerExecShell(node.Name, fmt.Sprintf(
			"curl -sSL '%s' | tar -xzf - -C /opt/cni/bin/ && cp /opt/cni/bin/macvlan /usr/lib/cni/macvlan 2>/dev/null; true", cniURL)); err != nil {
			return fmt.Errorf("installing CNI plugins on %s: %w", node.Name, err)
		}
		Logf("  %s: CNI plugins installed", node.Name)
	}

	// Install Calico (paths inside container via /repo mount)
	Logf("Installing Calico...")
	kubectl("apply", "-k", "/repo/e2e/calico")     //nolint:errcheck
	time.Sleep(3 * time.Second)                    // CRDs may not be established on first apply
	kubectl("apply", "-k", "/repo/e2e/calico")     //nolint:errcheck
	kubectl("wait", "--for=condition=established", //nolint:errcheck
		"crd/bgppeers.crd.projectcalico.org",
		"crd/bgpconfigurations.crd.projectcalico.org",
		"crd/felixconfigurations.crd.projectcalico.org",
		"crd/ippools.crd.projectcalico.org",
		"--timeout=60s")
	// Re-apply after CRDs are established to create CR resources (IPPools, BGPPeers, etc.)
	kubectl("apply", "-k", "/repo/e2e/calico") //nolint:errcheck

	// Install Coil
	Logf("Installing Coil...")
	kubectl("apply", "-f", "/repo/e2e/coil/crds.yaml")              //nolint:errcheck
	kubectl("apply", "-f", "/repo/e2e/coil/rbac.yaml")              //nolint:errcheck
	kubectl("apply", "-f", "/repo/e2e/coil/coild.yaml")             //nolint:errcheck
	kubectl("apply", "-f", "/repo/e2e/coil/egress-controller.yaml") //nolint:errcheck

	// Patch Egress CRD
	kubectl("patch", "crd", "egresses.coil.cybozu.com", "--type=json", //nolint:errcheck
		`-p=[{"op":"replace","path":"/spec/versions/0/schema/openAPIV3Schema/properties/spec/properties/template","value":{"type":"object","x-kubernetes-preserve-unknown-fields":true}}]`)

	// Calico IPPools for egress NAT
	Logf("Creating Calico IPPools for egress...")
	egressPools := `apiVersion: crd.projectcalico.org/v1
kind: IPPool
metadata:
  name: m2m-egress-v4
spec:
  cidr: 10.250.2.0/30
  natOutgoing: false
  blockSize: 32
  nodeSelector: "!all()"
  allowedUses:
    - Workload
---
apiVersion: crd.projectcalico.org/v1
kind: IPPool
metadata:
  name: m2m-egress-v6
spec:
  cidr: fd90:4dbf:396d::/126
  natOutgoing: false
  blockSize: 128
  nodeSelector: "!all()"
  allowedUses:
    - Workload
---
apiVersion: crd.projectcalico.org/v1
kind: IPPool
metadata:
  name: c2m-egress-v4
spec:
  cidr: 10.250.6.0/30
  natOutgoing: false
  blockSize: 32
  nodeSelector: "!all()"
  allowedUses:
    - Workload
---
apiVersion: crd.projectcalico.org/v1
kind: IPPool
metadata:
  name: c2m-egress-v6
spec:
  cidr: fde8:e5cf:d314::/126
  natOutgoing: false
  blockSize: 128
  nodeSelector: "!all()"
  allowedUses:
    - Workload`
	DockerExecInput(cp.Name, egressPools, "kubectl", "--kubeconfig="+kubeconfigPath, "apply", "-f", "-") //nolint:errcheck

	// IPv4 EndpointSlice — API server is now on overlay IP from the start
	Logf("Creating IPv4 EndpointSlice for kubernetes service...")
	epSlice := fmt.Sprintf(`apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: kubernetes-ipv4
  namespace: default
  labels:
    kubernetes.io/service-name: kubernetes
addressType: IPv4
endpoints:
  - addresses:
      - "%s"
    conditions:
      ready: true
ports:
  - name: https
    port: 6443
    protocol: TCP`, cp.IPv4)
	DockerExecInput(cp.Name, epSlice, "kubectl", "--kubeconfig="+kubeconfigPath, "apply", "-f", "-") //nolint:errcheck

	// Multus
	multusVersion := EnvOr("MULTUS_VERSION", "v4.1.4")
	Logf("Installing Multus %s...", multusVersion)
	kubectl("apply", "-f", //nolint:errcheck
		fmt.Sprintf("https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/%s/deployments/multus-daemonset-thick.yml", multusVersion))
	kubectl("-n", "kube-system", "patch", "daemonset", "kube-multus-ds", "--type=json", //nolint:errcheck
		`-p=[{"op":"replace","path":"/spec/template/spec/containers/0/resources/limits/memory","value":"512Mi"},{"op":"replace","path":"/spec/template/spec/containers/0/resources/requests/memory","value":"512Mi"}]`)

	// MetalLB (controller only, no speaker — kube-vip handles VIP BGP)
	metallbVersion := EnvOr("METALLB_VERSION", "v0.14.9")
	Logf("Installing MetalLB %s (controller only)...", metallbVersion)
	kubectl("apply", "-f", //nolint:errcheck
		fmt.Sprintf("https://raw.githubusercontent.com/metallb/metallb/%s/config/manifests/metallb-native.yaml", metallbVersion))
	kubectl("wait", "--for=condition=available", "deployment/controller", //nolint:errcheck
		"-n", "metallb-system", "--timeout=120s")
	kubectl("delete", "daemonset", "speaker", "-n", "metallb-system", "--ignore-not-found=true") //nolint:errcheck
	// Remove the webhook too — it targets the speaker which we don't run.
	kubectl("delete", "validatingwebhookconfiguration", "metallb-webhook-configuration", "--ignore-not-found=true") //nolint:errcheck

	// kube-vip DaemonSet
	Logf("Installing kube-vip DaemonSet...")
	kubectl("apply", "-f", "/repo/e2e/kube-vip/kube-vip-ds.yaml") //nolint:errcheck

	// Patch CoreDNS to forward to NAT64 unbound
	Logf("Patching CoreDNS → NAT64 DNS...")
	coreDNSPatch := fmt.Sprintf(
		`kubectl --kubeconfig=%s get configmap coredns -n kube-system -o json | `+
			`jq --arg dns "forward . %s" '.data.Corefile |= gsub("forward \\. /etc/resolv\\.conf(\\s*\\{[^}]*\\})?"; $dns)' | `+
			`kubectl --kubeconfig=%s apply -f -`,
		kubeconfigPath, cluster.NAT64DNS, kubeconfigPath)
	DockerExecShell(cp.Name, coreDNSPatch) //nolint:errcheck

	// Operator + agents
	Logf("Installing operator + agents...")
	if _, err := DockerExecShell(cp.Name, fmt.Sprintf(
		"kubectl --kubeconfig=%s kustomize /repo/e2e/operator | kubectl --kubeconfig=%s apply -f -",
		kubeconfigPath, kubeconfigPath)); err != nil {
		return fmt.Errorf("installing operator: %w", err)
	}

	// Network-sync controller (CAPI CRD + RBAC + Deployment)
	Logf("Installing network-sync controller...")
	if _, err := DockerExecShell(cp.Name, fmt.Sprintf(
		"kubectl --kubeconfig=%s kustomize /repo/e2e/sync | kubectl --kubeconfig=%s apply -f -",
		kubeconfigPath, kubeconfigPath)); err != nil {
		return fmt.Errorf("installing network-sync: %w", err)
	}

	Logf("Component installation complete")
	return nil
}

// Phase 6: Wait for everything to be ready and extract kubeconfig.
func PhaseFinalize(cluster *Cluster, repoRoot string) error {
	Logf("Phase 6: Finalizing...")

	cp := cluster.ControlPlane()
	kubeconfigPath := "/etc/kubernetes/admin.conf"

	// Wait for nodes Ready
	Logf("Waiting for nodes to be Ready...")
	DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath, //nolint:errcheck
		"wait", "--for=condition=Ready", "nodes", "--all", "--timeout=300s")

	// Wait for kube-system pods
	Logf("Waiting for kube-system pods...")
	DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath, //nolint:errcheck
		"wait", "--for=condition=Ready", "pod", "--all", "-n", "kube-system", "--timeout=300s")

	// Wait for operator
	Logf("Waiting for operator deployment...")
	DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath, //nolint:errcheck
		"wait", "--for=condition=available", "deployment/network-operator-operator",
		"-n", "kube-system", "--timeout=120s")

	// Wait for webhook to be serving (operator needs time to generate self-signed cert)
	Logf("Waiting for webhook to be ready...")
	WaitFor("webhook ready", 120*time.Second, 5*time.Second, func() (bool, error) { //nolint:errcheck
		out, err := DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath,
			"get", "endpoints", "network-operator-webhook-service", "-n", "kube-system",
			"-o", "jsonpath={.subsets[0].addresses[0].ip}")
		return err == nil && strings.TrimSpace(out) != "", nil
	})

	// Wait for agent DaemonSet
	Logf("Waiting for agent DaemonSet...")
	WaitFor("agent DaemonSet", 120*time.Second, 5*time.Second, func() (bool, error) { //nolint:errcheck
		out, err := DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath,
			"get", "ds", "network-operator-agent-cra-frr", "-n", "kube-system",
			"-o", "jsonpath={.status.desiredNumberScheduled},{.status.numberReady}")
		if err != nil {
			return false, err
		}
		parts := strings.Split(out, ",")
		if len(parts) == 2 && parts[0] != "0" && parts[0] == parts[1] {
			Logf("  Agent DaemonSet ready (%s/%s)", parts[1], parts[0])
			return true, nil
		}
		return false, nil
	})

	// Extract kubeconfig
	if err := ExtractKubeconfig(repoRoot, cluster); err != nil {
		return err
	}

	// Verify from tester
	Logf("Verifying API access from tester...")
	out, err := DockerExec("clab-nwop-tester", "kubectl",
		"--kubeconfig=/repo/e2etests/.kubeconfig", "cluster-info")
	if err != nil {
		Logf("WARNING: tester cannot reach API: %v", err)
	} else {
		Logf("Tester can reach API: %s", strings.Split(out, "\n")[0])
	}

	Logf("E2E lab is ready!")
	return nil
}

// getCRAPID returns the PID of the CRA nspawn process inside a node container.
func getCRAPID(container string) (string, error) {
	out, err := DockerExecShell(container,
		`P=$(systemctl show cra.service -p MainPID --value); cat /proc/$P/task/*/children | head -1 | tr -d " \n"`)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// importImage imports a Docker image from the host Docker daemon into containerd inside a node.
func importImage(container, image string) error {
	// Save from host docker → copy to container → ctr import
	// Use /var/tmp/ inside the container (not /tmp which is tmpfs — docker cp
	// writes under the tmpfs mount, invisible to the running container).
	tmpFile := fmt.Sprintf("/tmp/e2e-img-%s.tar", strings.ReplaceAll(
		strings.ReplaceAll(image, "/", "_"), ":", "_"))

	// Save image to tar on host
	if err := RunCmd("docker", "save", "-o", tmpFile, image); err != nil {
		return fmt.Errorf("docker save %s: %w", image, err)
	}
	defer os.Remove(tmpFile)

	// Copy into container and import
	if err := DockerCopy(tmpFile, container, "/var/tmp/image.tar"); err != nil {
		return fmt.Errorf("docker cp to %s: %w", container, err)
	}
	if _, err := DockerExec(container,
		"ctr", "-n=k8s.io", "images", "import", "--all-platforms", "/var/tmp/image.tar"); err != nil {
		return fmt.Errorf("ctr import on %s: %w", container, err)
	}
	DockerExec(container, "rm", "-f", "/var/tmp/image.tar") //nolint:errcheck
	return nil
}

// PhaseCluster2 provisions the second (gateway) cluster end-to-end.
// It is a simplified single-node cluster: no kube-vip, no Coil/MetalLB,
// just CRA + underlay, kubeadm init, operator/agents, network configs + gateway pods.
func PhaseCluster2(cluster2 *Cluster, repoRoot string) error {
	Logf("=== Cluster-2 setup ===")
	node := cluster2.Nodes[0]

	// Phase C2-1: Underlay (CRA + hbn + BGP)
	if err := PhaseUnderlay(cluster2); err != nil {
		return fmt.Errorf("cluster2 underlay: %w", err)
	}

	// Phase C2-2: kubeadm init (single-node, no VIP, untaint control-plane)
	Logf("Cluster-2: Running kubeadm init on %s...", node.Name)
	if err := KubeadmInitSingleNode(cluster2); err != nil {
		return fmt.Errorf("cluster2 kubeadm: %w", err)
	}

	// Configure DNS → NAT64 (needed for fetching remote manifests like Calico)
	Logf("Cluster-2: Configuring DNS → NAT64 (%s)...", cluster2.NAT64DNS)
	if _, err := DockerExecShell(node.Name,
		fmt.Sprintf("printf 'nameserver %s\\n' > /etc/resolv.conf", cluster2.NAT64DNS),
	); err != nil {
		return fmt.Errorf("setting DNS on %s: %w", node.Name, err)
	}

	// Phase C2-3: Install components (images, CNI plugins, Calico, Multus, operator)
	if err := PhaseCluster2Components(cluster2, repoRoot); err != nil {
		return fmt.Errorf("cluster2 components: %w", err)
	}

	// Phase C2-4: Wait for operator, apply network configs, deploy gateway pods
	if err := PhaseCluster2Gateway(cluster2, repoRoot); err != nil {
		return fmt.Errorf("cluster2 gateway: %w", err)
	}

	// Extract kubeconfig for tests
	if err := ExtractCluster2Kubeconfig(repoRoot, cluster2); err != nil {
		return fmt.Errorf("cluster2 kubeconfig: %w", err)
	}

	Logf("=== Cluster-2 ready ===")
	return nil
}

// PhaseCluster2Components installs minimal components on cluster-2.
// No kube-vip, no Coil, no MetalLB — just operator + agents + Calico + Multus + CNI plugins.
func PhaseCluster2Components(cluster *Cluster, repoRoot string) error {
	Logf("Cluster-2: Installing components...")

	cp := cluster.ControlPlane()
	kubeconfigPath := "/etc/kubernetes/admin.conf"

	kubectl := func(args ...string) error {
		fullArgs := append([]string{"--kubeconfig=" + kubeconfigPath}, args...)
		_, err := DockerExec(cp.Name, append([]string{"kubectl"}, fullArgs...)...)
		return err
	}

	// Load images
	imgBase := EnvOr("IMG_BASE", "ghcr.io/telekom")
	images := []string{
		imgBase + "/das-schiff-network-operator:latest",
		imgBase + "/das-schiff-nwop-agent-cra-frr:latest",
		imgBase + "/das-schiff-nwop-agent-netplan:latest",
		imgBase + "/das-schiff-platform-coil:latest",
		imgBase + "/das-schiff-platform-metallb:latest",
	}
	for _, img := range images {
		Logf("  Loading %s on %s", filepath.Base(img), cp.Name)
		if err := importImage(cp.Name, img); err != nil {
			return fmt.Errorf("loading image %s: %w", img, err)
		}
	}

	// CNI plugins
	arch := runtime.GOARCH
	cniVersion := EnvOr("CNI_PLUGINS_VERSION", "v1.9.0")
	cniURL := fmt.Sprintf(
		"https://github.com/containernetworking/plugins/releases/download/%s/cni-plugins-linux-%s-%s.tgz",
		cniVersion, arch, cniVersion)
	if _, err := DockerExecShell(cp.Name, fmt.Sprintf(
		"curl -sSL '%s' | tar -xzf - -C /opt/cni/bin/ && cp /opt/cni/bin/macvlan /usr/lib/cni/macvlan 2>/dev/null; true", cniURL)); err != nil {
		return fmt.Errorf("installing CNI plugins: %w", err)
	}

	// Calico (cluster-2 overlay: no coil CNI chain, different autodetection CIDRs)
	kubectl("apply", "-k", "/repo/e2e/calico-cluster2") //nolint:errcheck
	time.Sleep(3 * time.Second)                         //nolint:mnd
	kubectl("apply", "-k", "/repo/e2e/calico-cluster2") //nolint:errcheck
	kubectl("wait", "--for=condition=established",      //nolint:errcheck
		"crd/bgppeers.crd.projectcalico.org",
		"crd/bgpconfigurations.crd.projectcalico.org",
		"crd/felixconfigurations.crd.projectcalico.org",
		"crd/ippools.crd.projectcalico.org",
		"--timeout=60s")
	// Re-apply after CRDs are established to create CR resources (IPPools, BGPPeers, etc.)
	kubectl("apply", "-k", "/repo/e2e/calico-cluster2") //nolint:errcheck

	// Multus
	multusVersion := EnvOr("MULTUS_VERSION", "v4.1.4")
	kubectl("apply", "-f", //nolint:errcheck
		fmt.Sprintf("https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/%s/deployments/multus-daemonset-thick.yml", multusVersion))
	kubectl("-n", "kube-system", "patch", "daemonset", "kube-multus-ds", "--type=json", //nolint:errcheck
		`-p=[{"op":"replace","path":"/spec/template/spec/containers/0/resources/limits/memory","value":"512Mi"},{"op":"replace","path":"/spec/template/spec/containers/0/resources/requests/memory","value":"512Mi"}]`)

	// Operator + agents
	if _, err := DockerExecShell(cp.Name, fmt.Sprintf(
		"kubectl --kubeconfig=%s kustomize /repo/e2e/operator | kubectl --kubeconfig=%s apply -f -",
		kubeconfigPath, kubeconfigPath)); err != nil {
		return fmt.Errorf("installing operator: %w", err)
	}

	// Enable intent reconciler on cluster-2 (cluster-2 always uses intent CRDs).
	Logf("Cluster-2: Enabling intent reconciler on operator...")
	if _, err := DockerExecShell(cp.Name, fmt.Sprintf(
		`kubectl --kubeconfig=%s -n kube-system patch deployment network-operator-operator --type=json `+
			`-p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--enable-intent-reconciler"}]'`,
		kubeconfigPath)); err != nil {
		return fmt.Errorf("patching operator for intent: %w", err)
	}

	// Wait for operator
	Logf("Cluster-2: Waiting for operator...")
	DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath, //nolint:errcheck
		"wait", "--for=condition=available", "deployment/network-operator-operator",
		"-n", "kube-system", "--timeout=120s")

	// Wait for agent DaemonSet
	WaitFor("cluster2 agent DaemonSet", 120*time.Second, 5*time.Second, func() (bool, error) { //nolint:errcheck
		out, err := DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath,
			"get", "ds", "network-operator-agent-cra-frr", "-n", "kube-system",
			"-o", "jsonpath={.status.desiredNumberScheduled},{.status.numberReady}")
		if err != nil {
			return false, err
		}
		parts := strings.Split(out, ",")
		return len(parts) == 2 && parts[0] != "0" && parts[0] == parts[1], nil
	})

	return nil
}

// PhaseCluster2Gateway applies the gateway network configs and deploys gateway pods.
func PhaseCluster2Gateway(cluster *Cluster, repoRoot string) error {
	Logf("Cluster-2: Deploying gateway network configs + pods...")

	cp := cluster.ControlPlane()
	kubeconfigPath := "/etc/kubernetes/admin.conf"

	// Apply cluster-2 intent CRDs (VRFs, Networks, Destinations, L2Attachments).
	// Retry because the webhook may not be serving yet even though the operator pod is Ready
	// (the readiness probe checks healthz, but the webhook cert generation takes a moment longer).
	Logf("Cluster-2: Applying network configs (waiting for webhook)...")
	if err := WaitFor("cluster2 network configs", 120*time.Second, 5*time.Second, func() (bool, error) {
		_, err := DockerExecShell(cp.Name, fmt.Sprintf(
			"kubectl --kubeconfig=%s apply -f /repo/e2etests/testdata/intent/cluster2-configs.yaml",
			kubeconfigPath))
		return err == nil, nil
	}); err != nil {
		return fmt.Errorf("applying cluster2 network configs: %w", err)
	}

	// Wait for VLANs to be created by the operator
	Logf("Cluster-2: Waiting for VLAN interfaces...")
	if err := WaitFor("cluster2 VLANs", 120*time.Second, 5*time.Second, func() (bool, error) {
		_, err := DockerExecShell(cp.Name,
			`P=$(systemctl show cra.service -p MainPID --value); `+
				`CRA_PID=$(cat /proc/$P/task/*/children | head -1 | tr -d " \n"); `+
				`nsenter -t $CRA_PID -m -n -- ip link show vlan.601 2>/dev/null && `+
				`nsenter -t $CRA_PID -m -n -- ip link show vlan.602 2>/dev/null`)
		return err == nil, nil
	}); err != nil {
		return fmt.Errorf("waiting for cluster2 VLANs: %w", err)
	}

	// Apply gateway NADs + pods
	if _, err := DockerExecShell(cp.Name, fmt.Sprintf(
		"kubectl --kubeconfig=%s apply -f /repo/e2etests/testdata/cluster2-gateway-pods.yaml",
		kubeconfigPath)); err != nil {
		return fmt.Errorf("applying gateway pods: %w", err)
	}

	// Wait for gateway pods to be ready
	Logf("Cluster-2: Waiting for gateway pods...")
	for _, pod := range []string{"m2m-gateway", "c2m-gateway"} {
		if err := WaitFor(fmt.Sprintf("cluster2 %s", pod), 120*time.Second, 5*time.Second, func() (bool, error) {
			out, err := DockerExec(cp.Name, "kubectl", "--kubeconfig="+kubeconfigPath,
				"get", "pod", pod, "-n", "e2e-gateways",
				"-o", "jsonpath={.status.phase}")
			if err != nil {
				return false, nil
			}
			return strings.TrimSpace(out) == "Running", nil
		}); err != nil {
			return fmt.Errorf("waiting for %s pod: %w", pod, err)
		}
	}

	Logf("Cluster-2: Gateway pods running")
	return nil
}

// ExtractCluster2Kubeconfig extracts the kubeconfig from cluster-2 for test access.
func ExtractCluster2Kubeconfig(repoRoot string, cluster *Cluster) error {
	cp := cluster.ControlPlane()

	Logf("Extracting cluster-2 kubeconfig...")
	kubeconfig, err := DockerExec(cp.Name, "cat", "/etc/kubernetes/admin.conf")
	if err != nil {
		return fmt.Errorf("extracting cluster2 kubeconfig: %w", err)
	}

	// Replace server address with the node overlay IP (reachable from tester)
	kubeconfig = strings.ReplaceAll(kubeconfig,
		fmt.Sprintf("server: https://[%s]:6443", cp.IPv6),
		fmt.Sprintf("server: https://%s:6443", cp.IPv4))
	kubeconfig = strings.ReplaceAll(kubeconfig,
		fmt.Sprintf("server: https://%s:6443", cluster.VIP),
		fmt.Sprintf("server: https://%s:6443", cp.IPv4))

	for _, rel := range []string{"e2etests/.kubeconfig-cluster2", "e2e/.kubeconfig-cluster2"} {
		path := fmt.Sprintf("%s/%s", repoRoot, rel)
		if err := os.WriteFile(path, []byte(kubeconfig), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
	}

	Logf("Cluster-2 kubeconfig written")
	return nil
}

// PhaseSyncSetup creates the management-cluster resources for the network-sync controller:
// namespace, kubeconfig Secret, and CAPI Cluster object on cluster-1, pointing to cluster-2.
func PhaseSyncSetup(cluster1, cluster2 *Cluster, repoRoot string) error {
	Logf("Phase 8: Configuring sync controller for cluster-2...")

	cp1 := cluster1.ControlPlane()
	cp2 := cluster2.ControlPlane()
	kubeconfigPath := "/etc/kubernetes/admin.conf"

	kubectl := func(args ...string) error {
		fullArgs := append([]string{"--kubeconfig=" + kubeconfigPath}, args...)
		_, err := DockerExec(cp1.Name, append([]string{"kubectl"}, fullArgs...)...)
		return err
	}

	// 1. Create sync namespace on cluster-1
	Logf("Creating sync namespace cluster-nwop2 on cluster-1...")
	kubectl("create", "namespace", "cluster-nwop2", "--dry-run=client", "-o", "yaml") //nolint:errcheck
	nsYAML := `apiVersion: v1
kind: Namespace
metadata:
  name: cluster-nwop2`
	DockerExecInput(cp1.Name, nsYAML, "kubectl", "--kubeconfig="+kubeconfigPath, "apply", "-f", "-") //nolint:errcheck

	// 2. Read cluster-2's kubeconfig and create a Secret on cluster-1
	Logf("Creating kubeconfig Secret for cluster-2 on cluster-1...")
	rawKubeconfig, err := DockerExec(cp2.Name, "cat", "/etc/kubernetes/admin.conf")
	if err != nil {
		return fmt.Errorf("reading cluster2 kubeconfig: %w", err)
	}
	// Replace server address with the overlay IPv4 (reachable from cluster-1)
	rawKubeconfig = strings.ReplaceAll(rawKubeconfig,
		fmt.Sprintf("server: https://[%s]:6443", cp2.IPv6),
		fmt.Sprintf("server: https://%s:6443", cp2.IPv4))
	rawKubeconfig = strings.ReplaceAll(rawKubeconfig,
		fmt.Sprintf("server: https://%s:6443", cluster2.VIP),
		fmt.Sprintf("server: https://%s:6443", cp2.IPv4))

	// Create the Secret with the kubeconfig as `data.value`
	// (CAPI convention: <cluster-name>-kubeconfig, key "value")
	secretYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: nwop2-kubeconfig
  namespace: cluster-nwop2
type: Opaque
stringData:
  value: |
%s`, indentLines(rawKubeconfig, 4))
	if err := DockerExecInput(cp1.Name, secretYAML,
		"kubectl", "--kubeconfig="+kubeconfigPath, "apply", "-f", "-"); err != nil {
		return fmt.Errorf("creating kubeconfig secret: %w", err)
	}

	// 3. Create CAPI Cluster object on cluster-1
	Logf("Creating CAPI Cluster object for nwop2...")
	clusterYAML := `apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: nwop2
  namespace: cluster-nwop2
spec:
  paused: false`
	if err := DockerExecInput(cp1.Name, clusterYAML,
		"kubectl", "--kubeconfig="+kubeconfigPath, "apply", "-f", "-"); err != nil {
		return fmt.Errorf("creating CAPI cluster: %w", err)
	}

	// 4. Wait for network-sync to pick up the cluster
	Logf("Waiting for network-sync deployment to be available...")
	kubectl("wait", "--for=condition=available", "deployment/network-sync", //nolint:errcheck
		"-n", "kube-system", "--timeout=120s")

	Logf("Sync controller configured for cluster-2")
	return nil
}

// indentLines indents each line of s by n spaces.
func indentLines(s string, n int) string {
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

// EnvOr returns the environment variable value or a fallback default.
func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
