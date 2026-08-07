package tests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Routed KubeVirt VM datapath. The VM's only data NIC is the cni-workload
// secondary network; its static /32 + /128 must be reachable from the fabric
// (leaf/DCGW) via the UNDERLAY BGP — not the EVPN overlay.
//
// The fixture (KubeVirt + NAD + VM) is provisioned by the opt-in
// setup.PhaseKubeVirt (E2E_KUBEVIRT=1 during `e2e-up`); the test skips when it
// is not present. Run with: make e2e-test-kubevirt.
var _ = Describe("Routed KubeVirt VM", Label("kubevirt", "routed"), func() {
	const (
		vmIPv4    = "10.201.0.10"
		vmIPv6    = "fd00:201::10"
		fabricCon = "clab-nwop-dcgw1" // a DCGW in the underlay fabric
	)

	// The VM may be scheduled on either worker, so the underlay RIB assertions
	// accept the prefix on whichever CRA is hosting it.
	craNodes := []string{"nwop-worker", "nwop-worker2"}

	var (
		f   *framework.Framework
		ctx context.Context
	)

	BeforeEach(func() {
		if os.Getenv("E2E_KUBEVIRT") == "" {
			Skip("routed KubeVirt fixture not provisioned (set E2E_KUBEVIRT during e2e-up)")
		}
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()
	})

	// pingFromFabric reports whether the VM answered at least one echo request.
	// The assertion is about reachability, not about zero loss: the first probe
	// after the neighbour entry expires is routinely dropped while ARP/ND
	// resolves, so requiring 3/3 would be flaky.
	var lastPingOutput string
	pingFromFabric := func(target string, v6 bool) bool {
		args := []string{"ping", "-c", "3", "-W", "2", target}
		if v6 {
			args = []string{"ping", "-6", "-c", "3", "-W", "2", target}
		}
		stdout, stderr, err := f.DockerExec(ctx, fabricCon, args)
		lastPingOutput = stdout + stderr
		return err == nil && !strings.Contains(stdout, "0 received")
	}

	inAnyCRAUnderlayRIB := func(query, want string) bool {
		for _, node := range craNodes {
			out, _ := f.VtyshExecOnKindNode(ctx, node, query)
			if strings.Contains(out, want) {
				return true
			}
		}
		return false
	}

	It("advertises the VM /32 and /128 into the underlay BGP", func() {
		By("checking the CRA-FRR underlay RIB carries the VM IPv4 /32")
		Eventually(func() bool {
			return inAnyCRAUnderlayRIB(
				fmt.Sprintf("show bgp ipv4 unicast %s/32", vmIPv4), vmIPv4)
		}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"VM /32 not present in CRA-FRR underlay BGP")

		By("checking the CRA-FRR underlay RIB carries the VM IPv6 /128")
		Eventually(func() bool {
			return inAnyCRAUnderlayRIB(
				fmt.Sprintf("show bgp ipv6 unicast %s/128", vmIPv6), vmIPv6)
		}).WithTimeout(90*time.Second).WithPolling(5*time.Second).Should(BeTrue(),
			"VM /128 not present in CRA-FRR underlay BGP")
	})

	It("is reachable from the fabric (DCGW) over IPv4 and IPv6 via the underlay", func() {
		// The guest configures its addresses from cloud-init, which takes a few
		// minutes on first boot, so this needs a generous timeout.
		By("pinging the VM IPv4 from " + fabricCon)
		Eventually(func() bool {
			return pingFromFabric(vmIPv4, false)
		}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(),
			func() string {
				return fmt.Sprintf("IPv4 ping to VM %s from %s failed; last output:\n%s",
					vmIPv4, fabricCon, lastPingOutput)
			})

		By("pinging the VM IPv6 from " + fabricCon)
		Eventually(func() bool {
			return pingFromFabric(vmIPv6, true)
		}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(),
			func() string {
				return fmt.Sprintf("IPv6 ping to VM %s from %s failed; last output:\n%s",
					vmIPv6, fabricCon, lastPingOutput)
			})
	})
})
