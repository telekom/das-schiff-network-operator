package setup

import (
	"fmt"
	"os"
	"strings"
)

const kubeadmToken = "abcdef.0123456789abcdef"

// kubeadmInitConfig generates the multi-document kubeadm config for the control-plane,
// modelled after kind's internal config generation (v1beta3).
func kubeadmInitConfig(cluster *Cluster) string {
	cp := cluster.ControlPlane()

	// IPv6-primary dual-stack: IPv6 first, then IPv4
	nodeAddress := cp.IPv6 + "," + cp.IPv4
	advertiseAddress := cp.IPv6

	return fmt.Sprintf(`apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
kubernetesVersion: v1.32.2
clusterName: "%s"
controlPlaneEndpoint: "%s:6443"
apiServer:
  certSANs:
    - "localhost"
    - "127.0.0.1"
    - "::1"
    - "%s"
    - "%s"
    - "%s"
controllerManager:
  extraArgs:
    bind-address: "::"
scheduler:
  extraArgs:
    bind-address: "::1"
networking:
  podSubnet: "%s"
  serviceSubnet: "%s"
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: InitConfiguration
bootstrapTokens:
  - token: "%s"
localAPIEndpoint:
  advertiseAddress: "%s"
  bindPort: 6443
nodeRegistration:
  criSocket: "unix:///run/containerd/containerd.sock"
  kubeletExtraArgs:
    node-ip: "%s"
skipPhases:
  - addon/kube-proxy
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
cgroupDriver: systemd
cgroupRoot: /kubelet
failSwapOn: false
address: "::"
healthzBindAddress: "::"
imageGCHighThresholdPercent: 100
evictionHard:
  nodefs.available: "0%%"
  nodefs.inodesFree: "0%%"
  imagefs.available: "0%%"
---
apiVersion: kubeproxy.config.k8s.io/v1alpha1
kind: KubeProxyConfiguration
mode: "iptables"
iptables:
  minSyncPeriod: 1s
conntrack:
  maxPerCore: 0
`,
		cluster.Name,
		cluster.VIP,
		cluster.VIP,
		cp.IPv4,
		cp.IPv6,
		cluster.PodSubnet,
		cluster.ServiceSubnet,
		kubeadmToken,
		advertiseAddress,
		nodeAddress,
	)
}

// kubeadmJoinConfig generates the kubeadm join config for a worker node.
func kubeadmJoinConfig(cluster *Cluster, node *Node) string {
	nodeAddress := node.IPv6 + "," + node.IPv4

	return fmt.Sprintf(`apiVersion: kubeadm.k8s.io/v1beta3
kind: JoinConfiguration
nodeRegistration:
  criSocket: "unix:///run/containerd/containerd.sock"
  kubeletExtraArgs:
    node-ip: "%s"
discovery:
  bootstrapToken:
    apiServerEndpoint: "%s:6443"
    token: "%s"
    unsafeSkipCAVerification: true
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
cgroupDriver: systemd
cgroupRoot: /kubelet
failSwapOn: false
address: "::"
healthzBindAddress: "::"
imageGCHighThresholdPercent: 100
evictionHard:
  nodefs.available: "0%%"
  nodefs.inodesFree: "0%%"
  imagefs.available: "0%%"
`,
		nodeAddress,
		cluster.VIP,
		kubeadmToken,
	)
}

// KubeadmInit runs kubeadm init on the control-plane node.
func KubeadmInit(cluster *Cluster) error {
	cp := cluster.ControlPlane()
	config := kubeadmInitConfig(cluster)

	Logf("Running kubeadm init on %s...", cp.Name)

	// Write config inside the node
	if err := DockerExecInput(cp.Name, config, "tee", "/tmp/kubeadm.conf"); err != nil {
		return fmt.Errorf("writing kubeadm config: %w", err)
	}

	out, err := DockerExec(cp.Name,
		"kubeadm", "init",
		"--config=/tmp/kubeadm.conf",
		"--v=5",
	)
	if err != nil {
		return fmt.Errorf("kubeadm init: %w\n%s", err, out)
	}

	Logf("kubeadm init complete")
	return nil
}

// KubeadmJoin runs kubeadm join on a worker node.
func KubeadmJoin(cluster *Cluster, node *Node) error {
	config := kubeadmJoinConfig(cluster, node)

	Logf("Running kubeadm join on %s...", node.Name)

	// Write config inside the node
	if err := DockerExecInput(node.Name, config, "tee", "/tmp/kubeadm.conf"); err != nil {
		return fmt.Errorf("writing join config on %s: %w", node.Name, err)
	}

	out, err := DockerExec(node.Name,
		"kubeadm", "join",
		"--config=/tmp/kubeadm.conf",
		"--v=5",
	)
	if err != nil {
		return fmt.Errorf("kubeadm join on %s: %w\n%s", node.Name, err, out)
	}

	Logf("kubeadm join complete on %s", node.Name)
	return nil
}

// InstallKubeProxy installs kube-proxy via kubeadm on the control-plane.
func InstallKubeProxy(cluster *Cluster) error {
	cp := cluster.ControlPlane()
	Logf("Installing kube-proxy...")
	out, err := DockerExec(cp.Name, "kubeadm", "init", "phase", "addon", "kube-proxy",
		"--config=/tmp/kubeadm.conf")
	if err != nil {
		return fmt.Errorf("kube-proxy install: %w\n%s", err, out)
	}
	return nil
}

// ExtractKubeconfig extracts the admin kubeconfig from the control-plane
// and writes it for the tester container.
func ExtractKubeconfig(repoRoot string, cluster *Cluster) error {
	cp := cluster.ControlPlane()

	Logf("Extracting kubeconfig...")
	kubeconfig, err := DockerExec(cp.Name, "cat", "/etc/kubernetes/admin.conf")
	if err != nil {
		return fmt.Errorf("extracting kubeconfig: %w", err)
	}

	// Ensure server points to VIP. kubeadm uses controlPlaneEndpoint which is
	// already the VIP, but normalize any IPv6-bracketed form just in case.
	kubeconfig = strings.ReplaceAll(kubeconfig,
		fmt.Sprintf("server: https://[%s]:6443", cp.IPv6),
		fmt.Sprintf("server: https://%s:6443", cluster.VIP))

	// Write to e2etests/.kubeconfig and e2e/.kubeconfig
	for _, rel := range []string{"e2etests/.kubeconfig", "e2e/.kubeconfig"} {
		path := fmt.Sprintf("%s/%s", repoRoot, rel)
		if err := os.WriteFile(path, []byte(kubeconfig), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
	}

	Logf("Kubeconfig written to e2etests/.kubeconfig")
	return nil
}
