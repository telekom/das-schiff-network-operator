package tests

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// TC-12: gratuitous ARP for an address the router does not know yet does not
// create a neighbor entry, unless the network sets acceptGratuitousARP.
// It does not test whether an existing entry keeps its MAC address.
// The e2e agent runs with NWOP_L2_ACCEPT_GARP=never; VLAN 503 (vlan3) opts in,
// VLAN 502 (vlan2) does not.
var _ = Describe("Gratuitous ARP neighbor creation", Label("garp"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-garp"
	)

	// craExec runs a shell command in the CRA network namespace of a kind node.
	craExec := func(node, cmd string) string {
		out, stderr, err := f.DockerExec(ctx, node, []string{"bash", "-c",
			`set -e; P=$(systemctl show cra.service -p MainPID --value); ` +
				`CRA_PID=$(cat /proc/$P/task/*/children | head -1 | tr -d " \n"); ` +
				`test -n "$CRA_PID"; nsenter -t $CRA_PID -m -n -- ` + cmd})
		Expect(err).NotTo(HaveOccurred(), "CRA exec %q failed: %s", cmd, stderr)
		return strings.TrimSpace(out)
	}

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying VLAN 502/503 NADs")
		for _, path := range []string{"l2-connectivity/nad.yaml", "l2-connectivity/nad-c2m.yaml"} {
			nad, err := readTestdata(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())
		}
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "garp-502")
		_ = f.DeletePod(ctx, ns, "garp-503")
	})

	// announce starts a pod on VLAN vlan with podIP, makes it claim claimIP and
	// sends gratuitous ARP for claimIP. It returns the pod's MAC address.
	announce := func(vlan int, podIP, claimIP string) string {
		cfg := f.Config
		pod := fmt.Sprintf("garp-%d", vlan)
		Expect(f.CreateTestPod(ctx, ns, pod, cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(`[{"name": "macvlan-vlan%d", "ips": ["%s/24"]}]`, vlan, podIP),
		}, framework.WithNetAdmin())).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, pod, cfg.PodReadyTimeout)).To(Succeed())

		mac, _, err := f.ExecInPod(ctx, ns, pod, "", []string{"cat", "/sys/class/net/net1/address"})
		Expect(err).NotTo(HaveOccurred())

		_, _, err = f.ExecInPod(ctx, ns, pod, "", []string{"ip", "addr", "add", claimIP + "/32", "dev", "net1"})
		Expect(err).NotTo(HaveOccurred())
		// busybox arping exits 0 in unsolicited mode even without replies.
		_, stderr, err := f.ExecInPod(ctx, ns, pod, "", []string{"arping", "-U", "-c", "3", "-I", "net1", claimIP})
		Expect(err).NotTo(HaveOccurred(), "arping failed: %s", stderr)
		return strings.TrimSpace(mac)
	}

	It("creates a neighbor entry from gratuitous ARP only on networks that opt in", func() {
		node := f.Config.WorkerNode1

		By("Checking arp_accept on the CRA bridges")
		Expect(craExec(node, "cat /proc/sys/net/ipv4/conf/l2.502/arp_accept")).To(Equal("0"))
		Expect(craExec(node, "cat /proc/sys/net/ipv4/conf/l2.503/arp_accept")).To(Equal("1"))

		By("Opt-in network (VLAN 503): gratuitous ARP creates a neighbor entry")
		mac := announce(503, "10.250.30.200", "10.250.30.201")
		Eventually(func() string {
			return craExec(node, "ip neigh show 10.250.30.201 dev l2.503")
		}).WithTimeout(15 * time.Second).WithPolling(time.Second).Should(ContainSubstring(mac))

		By("Default network (VLAN 502): gratuitous ARP does not create a neighbor entry")
		mac = announce(502, "10.250.1.200", "10.250.1.201")

		By("Checking that the pod reaches the CRA bridge: ARP for the gateway creates an entry")
		_, _, err := f.ExecInPod(ctx, ns, "garp-502", "", []string{"ping", "-c", "1", "-W", "2", "10.250.1.1"})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string {
			return craExec(node, "ip neigh show 10.250.1.200 dev l2.502")
		}).WithTimeout(15 * time.Second).WithPolling(time.Second).Should(ContainSubstring(mac))

		Consistently(func() string {
			return craExec(node, "ip neigh show 10.250.1.201 dev l2.502")
		}).WithTimeout(10 * time.Second).WithPolling(time.Second).ShouldNot(ContainSubstring("lladdr"))
	})
})
