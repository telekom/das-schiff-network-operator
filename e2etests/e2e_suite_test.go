package e2etests

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/config"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
	// Import test packages so their init() / Describe blocks register.
	_ "github.com/telekom/das-schiff-network-operator/e2etests/tests"
)

var f *framework.Framework

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "das-schiff-network-operator E2E Suite")
}

var _ = BeforeSuite(func() {
	cfg := config.LoadFromEnv()

	var err error
	f, err = framework.New(cfg)
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for all nodes to be Ready")
	Expect(f.WaitForNodesReady(cfg.NodeReadyTimeout)).To(Succeed())

	By("Waiting for CRA-FRR agent pods to be Running")
	Expect(f.WaitForDaemonSetReady("kube-system", "network-operator-agent-cra-frr", cfg.ComponentReadyTimeout)).To(Succeed())

	By("Waiting for agent-netplan pods to be Running")
	Expect(f.WaitForDaemonSetReady("kube-system", "network-operator-agent-netplan", cfg.ComponentReadyTimeout)).To(Succeed())

	By("Waiting for network-sync deployment to be Ready")
	Expect(f.WaitForDeploymentReady("kube-system", "network-sync", cfg.ComponentReadyTimeout)).To(Succeed())

	// Export framework to tests
	framework.Global = f

	By("Initializing cluster-2 client")
	Expect(f.InitCluster2()).To(Succeed())

	if cfg.IntentMode {
		By("Intent mode: enabling intent reconciler on operator")
		Expect(f.EnableIntentReconciler(context.Background())).To(Succeed())

		By("Intent mode: applying intent base-configs (VRFs, Networks, Destinations)")
		intentConfigs, err := config.ReadManifest("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		// Retry because the webhook needs time to start serving after the operator restart.
		Eventually(func() error {
			return f.ApplyManifest(context.Background(), intentConfigs)
		}).WithTimeout(120 * time.Second).WithPolling(5 * time.Second).Should(Succeed())

		By("Intent mode: waiting for intent reconciler to produce NNCs")
		Expect(f.WaitForIntentNNCs(context.Background(), 60*time.Second)).To(Succeed())

		By("Intent mode: waiting for NNCs to contain both m2m and c2m VRFs (CRA convergence)")
		Expect(f.WaitForNNCVRFs(context.Background(), cfg.WorkerNode1, []string{"m2m", "c2m"}, 120*time.Second)).To(Succeed())
	} else {
		By("Applying network-operator CRs (VRFs + L2 networks)")
		nwopConfigs, err := config.ReadManifest("network-operator-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(context.Background(), nwopConfigs)).To(Succeed())
	}
})

var _ = AfterSuite(func() {
	if f != nil {
		By("Cleaning up test namespaces")
		f.CleanupTestNamespaces()
	}
})
