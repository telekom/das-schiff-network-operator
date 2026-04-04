package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/telekom/das-schiff-network-operator/e2e/setup"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "e2e-setup",
		Short: "Manage the E2E lab environment",
	}

	rootCmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Create and provision the E2E lab",
			RunE: func(_ *cobra.Command, _ []string) error {
				return up(findRepoRoot())
			},
		},
		&cobra.Command{
			Use:   "down",
			Short: "Tear down the E2E lab",
			RunE: func(_ *cobra.Command, _ []string) error {
				return down(findRepoRoot())
			},
		},
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// findRepoRoot locates the repository root by walking up from CWD or using REPO_ROOT.
func findRepoRoot() string {
	if v := os.Getenv("REPO_ROOT"); v != "" {
		return v
	}
	cwd, _ := os.Getwd()
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir
		}
		if dir == filepath.Dir(dir) {
			return cwd
		}
	}
}

func up(repoRoot string) error {
	start := time.Now()
	cluster := setup.DefaultCluster()
	cluster2 := setup.Cluster2()

	// Build images
	if err := setup.PhaseBuildImages(repoRoot); err != nil {
		return fmt.Errorf("build images: %w", err)
	}

	// Phase 0: Generate per-node configs (both clusters)
	setup.Logf("Phase 0: Generating configs...")
	if err := setup.GenerateNodeConfigs(repoRoot, cluster, cluster2); err != nil {
		return fmt.Errorf("generating configs: %w", err)
	}

	// Phase 1a: Create k8s node containers (both clusters)
	if err := setup.PhaseCreateNodes(cluster, repoRoot); err != nil {
		return fmt.Errorf("create nodes: %w", err)
	}
	if err := setup.PhaseCreateNodes(cluster2, repoRoot); err != nil {
		return fmt.Errorf("create cluster2 nodes: %w", err)
	}

	// Phase 1b: Deploy containerlab (fabric/infra + wire k8s ext-containers)
	if err := setup.PhaseContainerlab(repoRoot); err != nil {
		return fmt.Errorf("containerlab: %w", err)
	}

	// Phase 2: Start CRA, configure underlay, wait for BGP
	if err := setup.PhaseUnderlay(cluster); err != nil {
		return fmt.Errorf("underlay: %w", err)
	}

	// Phase 3: NAT64, DNS, kube-vip, rp_filter
	if err := setup.PhasePreKubeadm(cluster, repoRoot); err != nil {
		return fmt.Errorf("pre-kubeadm: %w", err)
	}

	// Phase 4: kubeadm init + join
	if err := setup.PhaseKubeadm(cluster); err != nil {
		return fmt.Errorf("kubeadm: %w", err)
	}

	// Phase 5: Install cluster components
	if err := setup.PhaseComponents(cluster, repoRoot); err != nil {
		return fmt.Errorf("components: %w", err)
	}

	// Phase 6: Wait ready + extract kubeconfig
	if err := setup.PhaseFinalize(cluster, repoRoot); err != nil {
		return fmt.Errorf("finalize: %w", err)
	}

	// Phase 7: Cluster-2 (gateway cluster)
	if err := setup.PhaseCluster2(cluster2, repoRoot); err != nil {
		return fmt.Errorf("cluster2: %w", err)
	}

	// Phase 8: Configure sync controller (create namespace + kubeconfig Secret + CAPI Cluster on cluster-1)
	if err := setup.PhaseSyncSetup(cluster, cluster2, repoRoot); err != nil {
		return fmt.Errorf("sync setup: %w", err)
	}

	setup.Logf("E2E lab ready in %v", time.Since(start).Round(time.Second))
	return nil
}

func down(repoRoot string) error {
	setup.Logf("Tearing down E2E lab...")

	cluster := setup.DefaultCluster()
	cluster2 := setup.Cluster2()
	topoDir := filepath.Join(repoRoot, "e2e", "setup")
	clabImage := setup.EnvOr("CLAB_IMAGE", "ghcr.io/srl-labs/clab:0.74.0")

	// Destroy containerlab topology (removes fabric/infra containers)
	setup.Logf("Destroying containerlab topology...")
	setup.RunCmd("docker", "run", "--rm", //nolint:errcheck
		"--privileged",
		"--network", "host",
		"--pid", "host",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", topoDir+":"+topoDir,
		"-w", topoDir,
		clabImage,
		"containerlab", "destroy", "--topo", "topology.clab.yml", "--cleanup",
	)

	// Remove k8s node containers (ext-container, not managed by clab destroy)
	setup.Logf("Removing k8s node containers...")
	for _, node := range cluster.Nodes {
		setup.RunCmd("docker", "rm", "-f", node.Name)                            //nolint:errcheck
		setup.RunCmd("docker", "network", "disconnect", "-f", "none", node.Name) //nolint:errcheck
	}
	for _, node := range cluster2.Nodes {
		setup.RunCmd("docker", "rm", "-f", node.Name)                            //nolint:errcheck
		setup.RunCmd("docker", "network", "disconnect", "-f", "none", node.Name) //nolint:errcheck
	}

	// Clean stale clab network endpoints
	for _, name := range []string{"clab-nwop-leaf1", "clab-nwop-leaf2", "clab-nwop-dcgw1", "clab-nwop-dcgw2", "clab-nwop-nat64", "clab-nwop-tester"} {
		setup.RunCmd("docker", "network", "disconnect", "-f", "none", name) //nolint:errcheck
	}

	// Prune anonymous volumes left by --volume /var
	setup.RunCmd("docker", "volume", "prune", "-f") //nolint:errcheck

	setup.Logf("E2E lab torn down.")
	return nil
}
