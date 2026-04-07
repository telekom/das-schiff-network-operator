package e2etests

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/config"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
	_ "github.com/telekom/das-schiff-network-operator/e2etests/tests" // Import test packages so their init() / Describe blocks register.
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

	By("Waiting for HBN-L2 agent pods to be Running")
	Expect(f.WaitForDaemonSetReady("kube-system", "network-operator-agent-hbn-l2", cfg.ComponentReadyTimeout)).To(Succeed())

	// Export framework to tests
	framework.Global = f

	By("Initializing cluster-2 client")
	Expect(f.InitCluster2()).To(Succeed())

	By("Applying network-operator CRs (VRFs + L2 networks)")
	nwopConfigs, err := config.ReadManifest("network-operator-configs.yaml")
	Expect(err).NotTo(HaveOccurred())
	Expect(f.ApplyManifest(context.Background(), nwopConfigs)).To(Succeed())
})

var _ = AfterSuite(func() {
	if f != nil {
		By("Cleaning up test namespaces")
		f.CleanupTestNamespaces()
	}
})
