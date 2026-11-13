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

// TC-12: Layer 2 bridges reject gratuitous ARP unless the network sets
// acceptGratuitousARP. VLAN 501 (vlan1) sets it, VLAN 502 (vlan2) does not.
var _ = Describe("Gratuitous ARP", Label("garp"), func() {
	var (
		f   *framework.Framework
		ctx context.Context
		ns  = "e2e-test-garp"
	)

	// craExec runs a shell command in the CRA network namespace of a kind node.
	craExec := func(node, cmd string) string {
		out, _, _ := f.DockerExec(ctx, node, []string{"bash", "-c",
			`P=$(systemctl show cra.service -p MainPID --value 2>/dev/null); ` +
				`CRA_PID=$(cat /proc/$P/task/*/children 2>/dev/null | head -1 | tr -d " \n"); ` +
				`[ -n "$CRA_PID" ] && nsenter -t $CRA_PID -m -n -- ` + cmd})
		return strings.TrimSpace(out)
	}

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying VLAN 501/502 NADs")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())
	})

	AfterEach(func() {
		_ = f.DeletePod(ctx, ns, "garp-501")
		_ = f.DeletePod(ctx, ns, "garp-502")
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
		// No replies are expected, so the exit code is not checked. The opt-in
		// case below proves that the announcement goes out.
		_, _, _ = f.ExecInPod(ctx, ns, pod, "", []string{"arping", "-U", "-c", "3", "-I", "net1", claimIP})
		return strings.TrimSpace(mac)
	}

	It("rejects gratuitous ARP by default and accepts it when the network opts in", func() {
		node := f.Config.WorkerNode1

		By("Checking arp_accept on the CRA bridges")
		Expect(craExec(node, "cat /proc/sys/net/ipv4/conf/l2.502/arp_accept")).To(Equal("0"))
		Expect(craExec(node, "cat /proc/sys/net/ipv4/conf/l2.501/arp_accept")).To(Equal("1"))

		By("Opt-in network (VLAN 501): gratuitous ARP creates a neighbor entry")
		mac := announce(501, "10.250.0.200", "10.250.0.201")
		Eventually(func() string {
			return craExec(node, "ip neigh show 10.250.0.201 dev l2.501")
		}).WithTimeout(15 * time.Second).WithPolling(time.Second).Should(ContainSubstring(mac))

		By("Default network (VLAN 502): gratuitous ARP is ignored")
		announce(502, "10.250.1.200", "10.250.1.201")
		Consistently(func() string {
			return craExec(node, "ip neigh show 10.250.1.201 dev l2.502")
		}).WithTimeout(10 * time.Second).WithPolling(time.Second).ShouldNot(ContainSubstring("lladdr"))
	})
})
