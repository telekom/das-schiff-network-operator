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

// vhost-user KubeVirt VM datapath on the grout CRA.
//
// This is the routed-KubeVirt test's harder sibling: the VM's data NIC is not a
// veth into a kernel netns but a DPDK vhost-user socket, allocated by our own
// device plugin, bind-mounted crossed into the launcher pod, turned into an
// <interface type='vhostuser'> by an onDefineDomain hook, and terminated on the
// other end by a grout net_vhost port. Nothing in that chain is exercised by
// any other test, and most of its failure modes are silent -- a socket that
// connects but moves no packets looks exactly like a healthy one from the API.
// So the test asserts the chain at each link rather than only end to end.
//
// The fixture is provisioned by the opt-in setup.PhaseKubeVirtVhostUser
// (E2E_KUBEVIRT=1 E2E_KUBEVIRT_VHOSTUSER=1 E2E_CRA_FLAVOR=grout during
// `e2e-up`); the test skips when it is not present.
var _ = Describe("vhost-user KubeVirt VM (grout)", Label("kubevirt", "vhostuser", "grout"), func() {
	const (
		vmName   = "grout-vhostuser-vm"
		vmIPv4   = "10.250.0.12"
		vmIPv6   = "fd94:685b:30cf:501::12"
		l2Bridge = "br4000002"
		peerPod  = "vhostuser-l2-peer"
		peerIPv4 = "10.250.0.50"
		peerIPv6 = "fd94:685b:30cf:501::50"
		resource = "network.t-caas.telekom.com/virtio-user"
	)

	craNodes := []string{"nwop-worker", "nwop-worker2"}

	var (
		f   *framework.Framework
		ctx context.Context
	)

	BeforeEach(func() {
		if os.Getenv("E2E_KUBEVIRT") == "" || os.Getenv("E2E_KUBEVIRT_VHOSTUSER") == "" {
			Skip("vhost-user KubeVirt fixture not provisioned " +
				"(set E2E_KUBEVIRT=1 E2E_KUBEVIRT_VHOSTUSER=1 during e2e-up)")
		}
		f = framework.Global
		Expect(f).NotTo(BeNil())
		if !f.IsGrout() {
			Skip("vhost-user VM datapath needs a DPDK fast path (E2E_CRA_FLAVOR=grout)")
		}
		ctx = context.Background()
	})

	// grcli runs a grout CLI command inside a node's CRA container. There is no
	// framework helper for this yet because grout state has so far only been
	// asserted indirectly, through FRR. Note the CRA is a nerdctl container in
	// the "hbr" namespace, not a machinectl machine like cra-frr.
	grcli := func(node string, args ...string) string {
		cmd := []string{"sh", "-c", "PATH=/usr/local/bin:$PATH nerdctl -n hbr exec cra-grout grcli " +
			strings.Join(args, " ")}
		stdout, _, _ := f.DockerExec(ctx, node, cmd)
		return stdout
	}

	// onAnyCRA runs check against each node that could be hosting the VM and
	// reports whether any of them satisfied it. The VM may land on either
	// worker and the fixture does not pin it.
	onAnyCRA := func(check func(node string) bool) bool {
		for _, node := range craNodes {
			if check(node) {
				return true
			}
		}
		return false
	}

	It("advertises the virtio-user resource on every node", func() {
		// If this fails nothing downstream can work: the VM would be
		// unschedulable, or worse, scheduled with an empty deviceID.
		out, err := f.KubectlGet(ctx, "nodes", "-o",
			`jsonpath={range .items[*]}{.metadata.name}={.status.allocatable.`+
				strings.ReplaceAll(resource, ".", `\.`)+`}{"\n"}{end}`)
		Expect(err).NotTo(HaveOccurred())
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			name, count, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || name == "" {
				continue
			}
			Expect(count).NotTo(BeEmpty(), "node %s does not advertise %s", name, resource)
			Expect(count).NotTo(Equal("0"), "node %s advertises 0 of %s", name, resource)
		}
	})

	It("allocated a device and published the pod-side socket path", func() {
		By("reading the Multus network-status of the launcher pod")
		// The launcher POD is where Multus writes network-status; KubeVirt does
		// not copy it onto the VMI, which is exactly why the hook is told the
		// path by an annotation instead of discovering it here.
		var status string
		Eventually(func() string {
			status, _ = f.KubectlGet(ctx, "pods", "-n", "default",
				"-l", "kubevirt.io/vm="+vmName,
				"-o", `jsonpath={.items[0].metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}`)
			return status
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(ContainSubstring("vhost-user"),
			"launcher pod has no vhost-user device-info in its network-status; "+
				"Multus did not stage the device plugin's file")

		By("checking the published path is the POD-side one")
		// The pod-side tree with an ordinal index, never the host tree with a
		// device id. Publishing the host path yields a domain XML pointing at a
		// socket the launcher cannot open -- and the VM still starts.
		Expect(status).To(ContainSubstring("/run/vsr-virtio-user/0/socket"),
			"device-info does not carry the pod-side socket path: %s", status)
		Expect(status).NotTo(ContainSubstring("/run/vsr-vhost-user/"),
			"device-info leaked a host-side path into the pod: %s", status)

		By("checking the mode is stated from the workload's perspective")
		// Whitespace-insensitive: whether the annotation arrives compact or
		// pretty-printed is Multus's business, not the contract's.
		Expect(strings.Join(strings.Fields(status), "")).To(ContainSubstring(`"mode":"client"`),
			"a virtio-user workload must be the vhost-user client: %s", status)
	})

	It("created the socket directory on the host and a grout net_vhost port", func() {
		By("finding a CRA whose grout has a vhost port for the VM")
		Eventually(func() bool {
			return onAnyCRA(func(node string) bool {
				return strings.Contains(grcli(node, "interface", "show"), "net_vhost")
			})
		}).WithTimeout(3*time.Minute).WithPolling(10*time.Second).Should(BeTrue(),
			"no grout net_vhost port was created for the vhost-user attachment")

		By("checking the socket itself exists in the host-side tree")
		// The device plugin makes the directory; grout, as the vhost-user
		// server, is what actually creates the socket inode in it. Its presence
		// is the first evidence the two ends agreed on a path.
		Eventually(func() bool {
			return onAnyCRA(func(node string) bool {
				stdout, _, _ := f.DockerExec(ctx, node,
					[]string{"bash", "-c", "ls /run/vsr-vhost-user/*/socket 2>/dev/null"})
				return strings.Contains(stdout, "/socket")
			})
		}).WithTimeout(2*time.Minute).WithPolling(10*time.Second).Should(BeTrue(),
			"no vhost-user socket appeared in /run/vsr-vhost-user/<deviceID>/")
	})

	It("wired the vhost-user interface into the libvirt domain", func() {
		// If the hook silently did nothing, KubeVirt would have started the VM
		// with its generated bridge interface instead and every other assertion
		// here would still pass -- except the pings.
		Eventually(func() string {
			out, _ := f.KubectlGet(ctx, "vmi", vmName, "-n", "default",
				"-o", "jsonpath={.status.interfaces[0].name}")
			return out
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Equal("vhostuser"))

		// Not skippable on error: this is the only assertion that proves the
		// hook ran, and skipping it would let a silently-unconverted domain
		// pass everything up to the ping.
		stdout, stderr, err := f.ExecInPod(ctx, "default", launcherPod(ctx, f, vmName), "compute",
			[]string{"virsh", "-c", "qemu:///session", "dumpxml", "default_" + vmName})
		Expect(err).NotTo(HaveOccurred(), "virsh dumpxml failed: %s", stderr)
		Expect(stdout).To(ContainSubstring("type='vhostuser'"),
			"the onDefineDomain hook did not convert the interface")
		Expect(stdout).To(ContainSubstring("/run/vsr-virtio-user/0/socket"))
		Expect(stdout).To(ContainSubstring("mode='shared'"),
			"guest memory is not shared, so vhost-user cannot map the rings")
	})

	It("enslaved the vhost-user port to the VLAN 501 bridge domain", func() {
		// L2 attach, not routed: the port carries no address of its own, so the
		// only evidence it is wired into the right broadcast domain is grout's
		// own view of the bridge membership. Without this the VM would still
		// have a working socket and simply talk to nobody.
		Eventually(func() bool {
			return onAnyCRA(func(node string) bool {
				out := grcli(node, "interface", "show")
				for _, line := range strings.Split(out, "\n") {
					if strings.Contains(line, "net_vhost") && strings.Contains(line, l2Bridge) {
						return true
					}
				}
				return false
			})
		}).WithTimeout(3*time.Minute).WithPolling(10*time.Second).Should(BeTrue(),
			"the vhost-user port was not enslaved to bridge domain %s", l2Bridge)
	})

	It("carries traffic over the vhost-user socket", func() {
		// The end-to-end assertion, and the only one that would catch a socket
		// that connected but never moved a packet.
		//
		// The peer is a pod in the same L2 domain rather than a fabric
		// container: an L2-attached VM has no /32 in the underlay, so it is
		// only reachable from inside VLAN 501 (or through the m2m VRF, which
		// terminates on cluster-2 in this lab). The pod is pinned to the node
		// the VM is NOT on, so every packet still crosses the VXLAN fabric:
		// pod -> macvlan on vlan.501 -> trunk -> leaf -> VXLAN -> the other
		// node's grout -> bridge domain -> vhost-user socket -> guest.
		vmNode := nodeOf(ctx, f, vmName)
		peerNode := craNodes[0]
		if peerNode == vmNode {
			peerNode = craNodes[1]
		}

		By("creating a VLAN 501 peer pod on " + peerNode)
		nad, err := readTestdata("kubevirt/macvlan-501.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, nad)).To(Succeed())
		DeferCleanup(func() {
			_ = f.DeletePod(ctx, "default", peerPod)
			_ = f.DeleteManifest(ctx, nad)
		})
		Expect(f.CreateTestPod(ctx, "default", peerPod, peerNode, map[string]string{
			"k8s.v1.cni.cncf.io/networks": `[{"name":"macvlan-501","ips":["` +
				peerIPv4 + `/24","` + peerIPv6 + `/64"]}]`,
		})).To(Succeed())
		Expect(f.WaitForPodReady(ctx, "default", peerPod, 2*time.Minute)).To(Succeed())

		var lastPing string
		pingFromPeer := func(target string) bool {
			stdout, stderr, err := f.ExecInPod(ctx, "default", peerPod, "tester",
				[]string{"ping", "-c", "3", "-W", "2", target})
			lastPing = stdout + stderr
			return err == nil && !strings.Contains(stdout, "0 packets received")
		}

		By("pinging the VM IPv4 from " + peerPod)
		Eventually(func() bool { return pingFromPeer(vmIPv4) }).
			WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(),
			func() string {
				return fmt.Sprintf("IPv4 ping to the vhost-user VM %s failed; last output:\n%s", vmIPv4, lastPing)
			})

		By("pinging the VM IPv6 from " + peerPod)
		Eventually(func() bool { return pingFromPeer(vmIPv6) }).
			WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(),
			func() string {
				return fmt.Sprintf("IPv6 ping to the vhost-user VM %s failed; last output:\n%s", vmIPv6, lastPing)
			})
	})
})

// nodeOf returns the node a VM's launcher pod is running on.
func nodeOf(ctx context.Context, f *framework.Framework, vmName string) string {
	out, err := f.KubectlGet(ctx, "pods", "-n", "default",
		"-l", "kubevirt.io/vm="+vmName, "-o", "jsonpath={.items[0].spec.nodeName}")
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// launcherPod returns the name of the virt-launcher pod backing a VM.
func launcherPod(ctx context.Context, f *framework.Framework, vmName string) string {
	out, err := f.KubectlGet(ctx, "pods", "-n", "default",
		"-l", "kubevirt.io/vm="+vmName, "-o", "jsonpath={.items[0].metadata.name}")
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}
