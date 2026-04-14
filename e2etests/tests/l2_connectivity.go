// Package tests contains all E2E test cases.
// Each file registers Describe blocks that are picked up by the suite.
package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-01: L2 Connectivity (same VLAN).
var _ = Describe("L2 Connectivity", Label("l2", "smoke"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-l2"
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying L2 NADs")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())
	})

	AfterEach(func() {
		By("Cleaning up test pods")
		_ = f.DeletePod(ctx, ns, "macvlan-01")
		_ = f.DeletePod(ctx, ns, "macvlan-02")
	})

	It("should allow ping between pods on the same VLAN across nodes", func() {
		cfg := f.Config

		By("Creating macvlan-01 on worker-1 (VLAN 501, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "macvlan-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan01IPv4, cfg.Macvlan01IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Creating macvlan-02 on worker-2 (VLAN 501, m2m)")
		Expect(f.CreateTestPod(ctx, ns, "macvlan-02", cfg.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(
				`[{"name": "macvlan-vlan501", "ips": ["%s/24", "%s/64"]}]`,
				cfg.Macvlan02IPv4, cfg.Macvlan02IPv6),
		}, framework.WithNetAdmin())).To(Succeed())

		By("Waiting for pods to be ready")
		Expect(f.WaitForPodReady(ctx, ns, "macvlan-01", cfg.PodReadyTimeout)).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "macvlan-02", cfg.PodReadyTimeout)).To(Succeed())

		By("Disabling IPv6 DAD and re-adding addresses")
		Expect(f.EnsureIPv6NoDad(ctx, ns, "macvlan-01", cfg.Macvlan01IPv6, "net1")).To(Succeed())
		Expect(f.EnsureIPv6NoDad(ctx, ns, "macvlan-02", cfg.Macvlan02IPv6, "net1")).To(Succeed())

		By("Verifying IPv4 connectivity: macvlan-01 → macvlan-02")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "macvlan-01", cfg.Macvlan02IPv4, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("IPv4 ping failed: %s\n", result.Output)
			}
			return result != nil && result.Success
		}).WithTimeout(60*time.Second).WithPolling(3*time.Second).Should(BeTrue(), "IPv4 ping failed")

		By("Verifying IPv6 connectivity: macvlan-01 → macvlan-02")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "macvlan-01", cfg.Macvlan02IPv6, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("IPv6 ping failed: %s\n", result.Output)
				// Dump pod neighbor + addr tables for debugging
				neighOut, _, _ := f.ExecInPod(ctx, ns, "macvlan-01", "", []string{"ip", "-6", "neigh", "show"})
				addrOut, _, _ := f.ExecInPod(ctx, ns, "macvlan-01", "", []string{"ip", "-6", "addr", "show"})
				routeOut, _, _ := f.ExecInPod(ctx, ns, "macvlan-01", "", []string{"ip", "-6", "route", "show"})
				GinkgoWriter.Printf("macvlan-01 IPv6 neigh:\n%s\n", neighOut)
				GinkgoWriter.Printf("macvlan-01 IPv6 addr:\n%s\n", addrOut)
				GinkgoWriter.Printf("macvlan-01 IPv6 route:\n%s\n", routeOut)
				// Dump CRA bridge neighbor tables on both nodes
				for _, node := range []string{cfg.WorkerNode1, cfg.WorkerNode2} {
					craNeigh, _, _ := f.DockerExec(ctx, node, []string{"bash", "-c",
						`P=$(systemctl show cra.service -p MainPID --value 2>/dev/null); ` +
							`CRA_PID=$(cat /proc/$P/task/*/children 2>/dev/null | head -1 | tr -d " \n"); ` +
							`[ -n "$CRA_PID" ] && nsenter -t $CRA_PID -m -n -- ip -6 neigh show dev l2.501`})
					GinkgoWriter.Printf("%s CRA l2.501 IPv6 neigh:\n%s\n", node, craNeigh)
					ndisc, _, _ := f.DockerExec(ctx, node, []string{"bash", "-c",
						`P=$(systemctl show cra.service -p MainPID --value 2>/dev/null); ` +
							`CRA_PID=$(cat /proc/$P/task/*/children 2>/dev/null | head -1 | tr -d " \n"); ` +
							`[ -n "$CRA_PID" ] && nsenter -t $CRA_PID -m -n -- cat /proc/sys/net/ipv6/conf/vlan.501/ndisc_notify 2>/dev/null`})
					GinkgoWriter.Printf("%s vlan.501 ndisc_notify: %s\n", node, ndisc)
				}
			}
			return result != nil && result.Success
		}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "IPv6 ping failed")

		By("Verifying IPv4 connectivity: macvlan-02 → macvlan-01")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "macvlan-02", cfg.Macvlan01IPv4, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("Reverse IPv4 ping failed: %s\n", result.Output)
			}
			return result != nil && result.Success
		}).WithTimeout(60*time.Second).WithPolling(3*time.Second).Should(BeTrue(), "Reverse IPv4 ping failed")

		By("Verifying IPv6 connectivity: macvlan-02 → macvlan-01")
		Eventually(func() bool {
			result, _ := f.PingFromPod(ctx, ns, "macvlan-02", cfg.Macvlan01IPv6, 1)
			if result != nil && !result.Success {
				GinkgoWriter.Printf("Reverse IPv6 ping failed: %s\n", result.Output)
				neighOut, _, _ := f.ExecInPod(ctx, ns, "macvlan-02", "", []string{"ip", "-6", "neigh", "show"})
				addrOut, _, _ := f.ExecInPod(ctx, ns, "macvlan-02", "", []string{"ip", "-6", "addr", "show"})
				GinkgoWriter.Printf("macvlan-02 IPv6 neigh:\n%s\n", neighOut)
				GinkgoWriter.Printf("macvlan-02 IPv6 addr:\n%s\n", addrOut)
			}
			return result != nil && result.Success
		}).WithTimeout(60*time.Second).WithPolling(5*time.Second).Should(BeTrue(), "Reverse IPv6 ping failed")
	})
})
