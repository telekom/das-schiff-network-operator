package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-11: VIP Failover with Gratuitous ARP/NA.
var _ = Describe("VIP Failover", Label("failover"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-failover"
	)

	// dumpNodeCRANeighbors dumps the CRA-internal bridge neighbor table for the given kind node.
	dumpNodeCRANeighbors := func(node string) {
		// nsenter into the CRA nspawn to see bridge neighbor table
		out, _, _ := f.DockerExec(ctx, node, []string{"bash", "-c",
			`P=$(systemctl show cra.service -p MainPID --value 2>/dev/null); ` +
				`CRA_PID=$(cat /proc/$P/task/*/children 2>/dev/null | head -1 | tr -d " \n"); ` +
				`[ -n "$CRA_PID" ] && nsenter -t $CRA_PID -m -n -- ip neigh show`})
		GinkgoWriter.Printf("%s CRA neighbor table:\n%s\n", node, out)
		out, _, _ = f.DockerExec(ctx, node, []string{"bash", "-c",
			`P=$(systemctl show cra.service -p MainPID --value 2>/dev/null); ` +
				`CRA_PID=$(cat /proc/$P/task/*/children 2>/dev/null | head -1 | tr -d " \n"); ` +
				`[ -n "$CRA_PID" ] && nsenter -t $CRA_PID -m -n -- bridge fdb show | grep -v "permanent\|33:33:" | head -30`})
		GinkgoWriter.Printf("%s CRA FDB (dynamic):\n%s\n", node, out)
	}

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying VLAN 501 NADs")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())
	})

	AfterEach(func() {
		By("Cleaning up test pods")
		_ = f.DeletePod(ctx, ns, "failover-src")
		_ = f.DeletePod(ctx, ns, "failover-dst")
	})

	It("should update neighbor caches when a VIP moves between nodes", func() {
		cfg := f.Config

		vipV4 := cfg.FailoverVIPv4
		vipV6 := cfg.FailoverVIPv6

		By("Creating failover-src on worker-1 (VLAN 501)")
		Expect(f.CreateTestPod(ctx, ns, "failover-src", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.FailoverPod01IPv4, cfg.FailoverPod01IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Creating failover-dst on worker-2 (VLAN 501)")
		Expect(f.CreateTestPod(ctx, ns, "failover-dst", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.FailoverPod02IPv4, cfg.FailoverPod02IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Waiting for pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "failover-src", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "failover-dst", cfg.PodReadyTimeout)).To(Succeed())

		By("Verifying baseline cross-node connectivity")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "failover-src", cfg.FailoverPod02IPv4, 1)
			return result != nil && result.Success
		}).WithTimeout(60*time.Second).WithPolling(3*time.Second).Should(BeTrue(), "baseline IPv4 cross-node ping failed")

		// --- Phase 1: Add VIP to failover-dst (worker-2) ---
		By("Adding VIP to failover-dst (worker-2)")
		_, _, err := f.ExecInPod(ctx, ns, "failover-dst", "", []string{
			"ip", "addr", "add", vipV4 + "/24", "dev", "net1",
		})
		Expect(err).NotTo(HaveOccurred())

		_, _, err = f.ExecInPod(ctx, ns, "failover-dst", "", []string{
			"ip", "addr", "add", vipV6 + "/64", "dev", "net1",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Sending gratuitous ARP from failover-dst")
		_, _, _ = f.ExecInPod(ctx, ns, "failover-dst", "", []string{
			"arping", "-c", "3", "-A", "-I", "net1", vipV4,
		})

		By("Verifying VIP IPv4 reachability from failover-src (cross-node)")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "failover-src", vipV4, 1)
			return result != nil && result.Success
		}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(BeTrue(), "VIP IPv4 not reachable on failover-dst")

		By("Verifying VIP IPv6 reachability from failover-src (cross-node)")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "failover-src", vipV6, 1)
			return result != nil && result.Success
		}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(BeTrue(), "VIP IPv6 not reachable on failover-dst")

		// --- Phase 2: Move VIP from failover-dst to failover-src ---
		By("Removing VIP from failover-dst")
		_, _, _ = f.ExecInPod(ctx, ns, "failover-dst", "", []string{
			"ip", "addr", "del", vipV4 + "/24", "dev", "net1",
		})
		_, _, _ = f.ExecInPod(ctx, ns, "failover-dst", "", []string{
			"ip", "addr", "del", vipV6 + "/64", "dev", "net1",
		})

		By("Adding VIP to failover-src (worker-1)")
		_, _, err = f.ExecInPod(ctx, ns, "failover-src", "", []string{
			"ip", "addr", "add", vipV4 + "/24", "dev", "net1",
		})
		Expect(err).NotTo(HaveOccurred())

		_, _, err = f.ExecInPod(ctx, ns, "failover-src", "", []string{
			"ip", "addr", "add", vipV6 + "/64", "dev", "net1",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Sending gratuitous ARP from failover-src")
		_, _, _ = f.ExecInPod(ctx, ns, "failover-src", "", []string{
			"arping", "-c", "3", "-A", "-I", "net1", vipV4,
		})

		// Also send unsolicited Neighbor Advertisement for IPv6.
		// ndisc6 is not available in busybox, so we use arping for IPv4 only
		// and rely on kernel NDP for IPv6. Send a ping6 to all-nodes multicast
		// from the VIP source address to trigger NDP on the remote side.
		_, _, _ = f.ExecInPod(ctx, ns, "failover-src", "", []string{
			"ping6", "-c", "1", "-W", "1", "-I", "net1", "ff02::1",
		})

		By("Verifying VIP IPv4 reachability after failover (from failover-dst, cross-node)")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "failover-dst", vipV4, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("VIP IPv4 failover ping failed: %s\n", result.Output)
				// Pod-level diagnostics
				out, _, _ := f.ExecInPod(ctx, ns, "failover-dst", "", []string{"ip", "neigh", "show"})
				GinkgoWriter.Printf("failover-dst neighbor table:\n%s\n", out)
				out, _, _ = f.ExecInPod(ctx, ns, "failover-src", "", []string{"ip", "addr", "show", "dev", "net1"})
				GinkgoWriter.Printf("failover-src net1 addrs:\n%s\n", out)
				// Node-level CRA diagnostics
				dumpNodeCRANeighbors(cfg.WorkerNode2)
			}
			return result != nil && result.Success
		}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(BeTrue(), "VIP IPv4 not reachable after failover")

		By("Verifying VIP IPv6 reachability after failover (from failover-dst, cross-node)")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "failover-dst", vipV6, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("VIP IPv6 failover ping failed: %s\n", result.Output)
			}
			return result != nil && result.Success
		}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(BeTrue(), "VIP IPv6 not reachable after failover")

		// --- Phase 3: Verify from m2mgw (external L3 path) ---
		By("Verifying VIP reachability from m2mgw after failover (IPv4)")
		Eventually(func() bool {
			result, _ := f.PingFromContainer(ctx, cfg.ClabM2MGW, vipV4, 1)
			return result != nil && result.Success
		}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(BeTrue(), "VIP IPv4 not reachable from m2mgw after failover")

		By("Verifying VIP reachability from m2mgw after failover (IPv6)")
		Eventually(func() bool {
			result, _ := f.PingFromContainer(ctx, cfg.ClabM2MGW, vipV6, 1)
			return result != nil && result.Success
		}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(BeTrue(), "VIP IPv6 not reachable from m2mgw after failover")

		// --- Cleanup: Remove VIP from failover-src ---
		By("Cleaning up VIP from failover-src")
		_, _, _ = f.ExecInPod(ctx, ns, "failover-src", "", []string{
			"ip", "addr", "del", vipV4 + "/24", "dev", "net1",
		})
		_, _, _ = f.ExecInPod(ctx, ns, "failover-src", "", []string{
			"ip", "addr", "del", vipV6 + "/64", "dev", "net1",
		})
	})
})
