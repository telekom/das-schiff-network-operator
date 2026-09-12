package tests

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// Workload CNI (cni-workload) attachments driven from plain pods.
//
// These cover the two attach modes that do not need a VM: an L2 access
// attachment into an existing Layer2 domain, an 802.1Q trunk carrying two
// domains (one of them under a translated VLAN id) on a single port, and the
// delegated IPAM lifecycle of a routed attachment. The routed *fabric* datapath
// (VM /32 into the underlay) is covered by the KubeVirt test instead.
//
// They are intent-labelled because the plugin resolves a Layer2Attachment by
// name: the L2 domains it binds to only exist once the intent pipeline has
// stamped them onto the NodeNetworkConfig.
var _ = Describe("Intent: Workload CNI attachments", Label("intent", "workloadcni"), func() {
	const (
		ns = "e2e-intent-wlcni"

		// Addresses of the workload-CNI pods themselves. The L2 ones must match
		// the static IPAM in testdata/workload-cni/nads.yaml.
		l2PodIPv4 = "10.250.0.60"
		l2PodIPv6 = "fd94:685b:30cf:501::60"
		// Trunk-side addresses, configured by the pod on its own VLAN
		// sub-interfaces: 501 keeps the fabric id, 502 is reached on id 200.
		trunkVLAN501IPv4 = "10.250.0.61/24"
		trunkVLAN502IPv4 = "10.250.1.61/24"

		// The host-local network name is the CNI config "name", which is also
		// the directory host-local keeps its allocations in.
		routedIPAMNetwork = "wl-routed-ipam"
	)

	var (
		f   *framework.Framework
		ctx context.Context
	)

	BeforeEach(func() {
		f = framework.Global
		Expect(f).NotTo(BeNil())
		ctx = context.Background()

		// The plugin binary is installed by the lab's workload-CNI phase, so a
		// missing installer is a lab regression, not a reason to pass silently.
		_, err := f.KubeClient.AppsV1().DaemonSets("kube-system").
			Get(ctx, "cni-workload-installer", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred(), "cni-workload installer DaemonSet missing — was the lab brought up with PhaseWorkloadCNI?")
		Expect(f.WaitForDaemonSetReady("kube-system", "cni-workload-installer",
			f.Config.ComponentReadyTimeout)).To(Succeed())

		By("Creating test namespace")
		Expect(f.CreateNamespace(ctx, ns)).To(Succeed())

		By("Applying intent base configs")
		base, err := readTestdata("intent/base-configs.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifest(ctx, base)).To(Succeed())

		By("Applying macvlan NADs for the peer pods")
		nad, err := readTestdata("l2-connectivity/nad.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, nad, ns)).To(Succeed())

		By("Applying workload-CNI NADs")
		wlNAD, err := readTestdata("workload-cni/nads.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.ApplyManifestInNamespace(ctx, wlNAD, ns)).To(Succeed())
	})

	AfterEach(func() {
		for _, pod := range []string{"wlcni-l2-01", "wlcni-trunk-01", "wlcni-ipam-01", "wlcni-peer-501", "wlcni-peer-502"} {
			_ = f.DeletePod(ctx, ns, pod)
		}
	})

	// createMacvlanPeer puts a plain macvlan pod on the given VLAN so the
	// workload-CNI pod has something to talk to inside the L2 domain.
	createMacvlanPeer := func(name, nad, ipv4, ipv6 string) {
		annotation := fmt.Sprintf(`[{"name": %q, "ips": ["%s/24", "%s/64"]}]`, nad, ipv4, ipv6)
		Expect(f.CreateTestPod(ctx, ns, name, f.Config.WorkerNode2, map[string]string{
			"k8s.v1.cni.cncf.io/networks": annotation,
		}, framework.WithNetAdmin())).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, name, f.Config.PodReadyTimeout)).To(Succeed())
		Expect(f.EnsureIPv6NoDad(ctx, ns, name, ipv6, "net1")).To(Succeed())
	}

	// pingEventually pings from a named interface. Pinning the source keeps the
	// assertion honest: without -I the kernel is free to pick the pod's primary
	// interface, and a ping that never touched the attachment would still pass.
	// The attachment is programmed asynchronously after CNI ADD, hence Eventually.
	pingEventually := func(pod, iface, target string, timeout time.Duration) {
		cmd := "ping"
		if strings.Contains(target, ":") {
			cmd = "ping6"
		}
		Eventually(func() error {
			stdout, stderr, err := f.ExecInPod(ctx, ns, pod, "tester",
				[]string{cmd, "-I", iface, "-c", "3", "-W", "3", target})
			if err != nil {
				GinkgoWriter.Printf("ping %s from %s via %s failed: %v\nstdout: %s\nstderr: %s\n",
					target, pod, iface, err, stdout, stderr)
			}
			return err
		}).WithTimeout(timeout).WithPolling(5*time.Second).
			Should(Succeed(), "ping %s from %s via %s never succeeded", target, pod, iface)
	}

	// expectDomainOwner asserts which Layer2Attachment the node's Layer2 domain
	// was stamped with. Several intent specs leave same-VLAN Layer2Attachments
	// behind and the builder lets only one of them own a VLAN, so a NAD
	// referencing the loser would attach to nothing — this turns that into an
	// immediate, readable failure instead of a ping timeout.
	expectDomainOwner := func(node, vlanKey, l2aName string) {
		Eventually(func() string {
			nnc, err := f.GetNNC(ctx, node)
			if err != nil {
				return ""
			}
			owner, _, _ := unstructured.NestedString(nnc.Object,
				"spec", "layer2s", vlanKey, "attachmentRef", "name")
			return owner
		}).WithTimeout(120*time.Second).WithPolling(5*time.Second).Should(Equal(l2aName),
			"VLAN %s on %s is not owned by %q — the workload-CNI NAD references it by name",
			vlanKey, node, l2aName)
	}

	// expectAddress waits for an address to show up on the pod's attached
	// interface. The attachment is programmed asynchronously after CNI ADD.
	expectAddress := func(pod, family, address string) {
		Eventually(func() string {
			out, _, _ := f.ExecInPod(ctx, ns, pod, "tester",
				[]string{"ip", family, "-o", "addr", "show", "net1"})
			return out
		}).WithTimeout(60*time.Second).WithPolling(3*time.Second).
			Should(ContainSubstring(address), "%s has no %s address on net1", pod, address)
	}

	It("attaches a pod into an existing Layer2 domain (access mode)", func() {
		cfg := f.Config

		By("Verifying VLAN 501 is owned by the referenced Layer2Attachment")
		expectDomainOwner(cfg.WorkerNode1, "501", "l2a-base-vlan501")

		By("Creating a macvlan peer on VLAN 501")
		createMacvlanPeer("wlcni-peer-501", "macvlan-vlan501", cfg.Macvlan02IPv4, cfg.Macvlan02IPv6)

		By("Creating the L2-attached pod on worker-1")
		Expect(f.CreateTestPod(ctx, ns, "wlcni-l2-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": `[{"name": "wl-l2-vlan501"}]`,
		}, framework.WithNetAdmin())).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "wlcni-l2-01", cfg.PodReadyTimeout)).To(Succeed())

		// Both families come from the NAD's static IPAM. The v6 address has to
		// be asserted before DAD is reset, because resetting it re-adds the
		// address and would mask an IPAM that never assigned it.
		By("Verifying the static IPAM addresses landed on net1")
		expectAddress("wlcni-l2-01", "-4", l2PodIPv4)
		expectAddress("wlcni-l2-01", "-6", l2PodIPv6)

		By("Disabling IPv6 DAD on the attached interface")
		Expect(f.EnsureIPv6NoDad(ctx, ns, "wlcni-l2-01", l2PodIPv6, "net1")).To(Succeed())

		// The port only reaches the domain once the agent has enslaved it into
		// the l2.501 bridge, which happens asynchronously after CNI ADD.
		By("Verifying L2 connectivity to the macvlan peer over IPv4")
		pingEventually("wlcni-l2-01", "net1", cfg.Macvlan02IPv4, 120*time.Second)

		By("Verifying L2 connectivity to the macvlan peer over IPv6")
		pingEventually("wlcni-l2-01", "net1", cfg.Macvlan02IPv6, 120*time.Second)
	})

	It("carries two Layer2 domains on one trunked port, one of them VLAN-translated", func() {
		cfg := f.Config

		By("Verifying both trunk members are owned by the referenced Layer2Attachments")
		expectDomainOwner(cfg.WorkerNode1, "501", "l2a-base-vlan501")
		expectDomainOwner(cfg.WorkerNode1, "502", "l2a-base-vlan502")

		By("Creating macvlan peers on VLAN 501 and VLAN 502")
		createMacvlanPeer("wlcni-peer-501", "macvlan-vlan501", cfg.Macvlan02IPv4, cfg.Macvlan02IPv6)
		createMacvlanPeer("wlcni-peer-502", "macvlan-vlan502", cfg.Macvlan03IPv4, cfg.Macvlan03IPv6)

		By("Creating the trunk-attached pod on worker-1")
		Expect(f.CreateTestPod(ctx, ns, "wlcni-trunk-01", cfg.WorkerNode1, map[string]string{
			"k8s.v1.cni.cncf.io/networks": `[{"name": "wl-l2-trunk"}]`,
		}, framework.WithNetAdmin())).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "wlcni-trunk-01", cfg.PodReadyTimeout)).To(Succeed())

		// Every member of a trunk is tagged, so the guest addresses VLAN
		// sub-interfaces rather than the port itself. VLAN 501 keeps its fabric
		// id; VLAN 502 is translated by the CRA and is reached on id 200 here.
		By("Configuring the guest-side VLAN sub-interfaces")
		for _, cmd := range [][]string{
			{"ip", "link", "set", "net1", "up"},
			{"ip", "link", "add", "link", "net1", "name", "net1.501", "type", "vlan", "id", "501"},
			{"ip", "addr", "add", trunkVLAN501IPv4, "dev", "net1.501"},
			{"ip", "link", "set", "net1.501", "up"},
			{"ip", "link", "add", "link", "net1", "name", "net1.200", "type", "vlan", "id", "200"},
			{"ip", "addr", "add", trunkVLAN502IPv4, "dev", "net1.200"},
			{"ip", "link", "set", "net1.200", "up"},
		} {
			_, stderr, err := f.ExecInPod(ctx, ns, "wlcni-trunk-01", "tester", cmd)
			Expect(err).NotTo(HaveOccurred(), "%v failed: %s", cmd, stderr)
		}

		By("Verifying the untranslated member (VLAN 501) reaches its peer")
		pingEventually("wlcni-trunk-01", "net1.501", cfg.Macvlan02IPv4, 120*time.Second)

		By("Verifying the translated member (fabric VLAN 502, guest VLAN 200) reaches its peer")
		pingEventually("wlcni-trunk-01", "net1.200", cfg.Macvlan03IPv4, 120*time.Second)
	})

	It("allocates delegated IPAM addresses and releases them on delete", func() {
		cfg := f.Config
		node := cfg.WorkerNode1

		By("Creating a routed pod with host-local IPAM")
		Expect(f.CreateTestPod(ctx, ns, "wlcni-ipam-01", node, map[string]string{
			"k8s.v1.cni.cncf.io/networks": `[{"name": "wl-routed-ipam"}]`,
		}, framework.WithNetAdmin())).To(Succeed())
		Expect(f.WaitForPodReady(ctx, ns, "wlcni-ipam-01", cfg.PodReadyTimeout)).To(Succeed())

		// host-local is configured with a range per family, so both must be
		// allocated — and both must be handed back again.
		By("Verifying the pod got an address out of each configured range")
		var podIPv4, podIPv6 string
		Eventually(func() string {
			out, _, _ := f.ExecInPod(ctx, ns, "wlcni-ipam-01", "tester",
				[]string{"ip", "-o", "addr", "show", "net1"})
			if m := regexp.MustCompile(`inet (10\.202\.0\.\d+)`).FindStringSubmatch(out); m != nil {
				podIPv4 = m[1]
			}
			if m := regexp.MustCompile(`inet6 (fd00:202::[0-9a-f:]+)`).FindStringSubmatch(out); m != nil {
				podIPv6 = m[1]
			}
			if podIPv4 == "" || podIPv6 == "" {
				return out
			}
			return "allocated"
		}).WithTimeout(60*time.Second).WithPolling(3*time.Second).
			Should(Equal("allocated"), "host-local did not assign both families on the attached interface")

		By("Verifying host-local recorded both allocations on the node")
		Eventually(func() []string {
			return hostLocalAllocations(ctx, f, node, routedIPAMNetwork)
		}).WithTimeout(30*time.Second).WithPolling(3*time.Second).
			Should(And(ContainElement(podIPv4), ContainElement(podIPv6)),
				"host-local did not record the allocations")

		By("Deleting the pod")
		Expect(f.DeletePod(ctx, ns, "wlcni-ipam-01")).To(Succeed())

		By("Verifying the allocations were released on CNI DEL")
		Eventually(func() []string {
			return hostLocalAllocations(ctx, f, node, routedIPAMNetwork)
		}).WithTimeout(90*time.Second).WithPolling(3*time.Second).
			ShouldNot(Or(ContainElement(podIPv4), ContainElement(podIPv6)),
				"host-local allocation leaked after pod deletion")
	})
})

// hostLocalAllocations lists the reservations the host-local IPAM plugin keeps
// for a network on a node. Reservation files are named after the allocated IP;
// host-local also keeps bookkeeping files (lock, last_reserved_ip.<range index>,
// one per configured range) which are filtered out so they cannot mask a leaked
// address. Exact file names are returned so that a prefix such as 10.202.0.2
// cannot be satisfied by an unrelated 10.202.0.20.
func hostLocalAllocations(ctx context.Context, f *framework.Framework, node, network string) []string {
	stdout, _, err := f.DockerExec(ctx, node, []string{"ls", "/var/lib/cni/networks/" + network})
	if err != nil {
		// The directory only exists once something was allocated.
		return nil
	}
	var entries []string
	for _, name := range strings.Fields(stdout) {
		if name == "lock" || strings.HasPrefix(name, "last_reserved_ip") {
			continue
		}
		entries = append(entries, name)
	}
	return entries
}
