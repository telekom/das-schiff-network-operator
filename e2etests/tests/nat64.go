package tests

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-10: NAT64 Outbound.
var _ = Describe("NAT64 Outbound", Label("nat64"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-nat64"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "nat64-tester")
	})

	It("should resolve and reach IPv4-only targets via NAT64", func() {
		cfg := f.Config

		By("Creating a test pod with DNS pointing to NAT64 unbound")
		Expect(f.CreateTestPod(ctx, ns, "nat64-tester", cfg.WorkerNode1, nil,
			framework.WithDNS([]string{cfg.NAT64DNS}))).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "nat64-tester", cfg.PodReadyTimeout)).To(Succeed())

		By("Resolving an A-only domain via DNS64")
		stdout, _, err := f.ExecInPod(ctx, ns, "nat64-tester", "",
			[]string{"nslookup", "-type=AAAA", "example.com"})
		Expect(err).NotTo(HaveOccurred())
		Expect(stdout).To(ContainSubstring("64:ff9b::"),
			"Expected synthesised AAAA with 64:ff9b:: prefix, got: %s", stdout)

		// TODO: Once NAT64 gateway is fully wired with routes from CRA-FRR,
		// test that the pod can actually curl an IPv4-only endpoint through
		// the synthesised IPv6 address.
	})
})
