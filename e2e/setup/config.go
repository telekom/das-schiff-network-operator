package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// GenerateNodeConfigs generates per-node CRA configs into e2e/generated/<node>/.
// These are bind-mounted into containers by containerlab.
func GenerateNodeConfigs(repoRoot string, clusters ...*Cluster) error {
	genDir := filepath.Join(repoRoot, "e2e", "generated")
	tplDir := filepath.Join(repoRoot, "e2e", "cra-config")

	// Clean and recreate
	if err := os.RemoveAll(genDir); err != nil {
		return fmt.Errorf("removing generated dir: %w", err)
	}

	for _, cluster := range clusters {
		// Determine VNI/RT based on cluster name
		clusterVNI, clusterRT := 30, "64497:30"
		mgmtVNI, mgmtRT := 20, "64497:20"
		if cluster.Name != "nwop" {
			// Cluster-2 uses different cluster VNI/RT to isolate EVPN domains.
			// Mgmt VNI/RT stays the same — shared management EVPN domain.
			clusterVNI, clusterRT = 31, "64497:31"
		}

		for _, node := range cluster.Nodes {
			if err := generateNodeConfig(genDir, tplDir, node, clusterVNI, clusterRT, mgmtVNI, mgmtRT, cluster.ExportCIDRv4, cluster.ExportCIDRv6, cluster.UnderlayVMv4, cluster.UnderlayVMv6); err != nil {
				return err
			}
		}
	}

	Logf("All configs generated in %s", genDir)
	return nil
}

func generateNodeConfig(genDir, tplDir string, node Node, clusterVNI int, clusterRT string, mgmtVNI int, mgmtRT string, exportCIDRv4, exportCIDRv6, underlayVMv4, underlayVMv6 string) error {
	shortName := strings.TrimPrefix(node.Name, "clab-nwop-")
	nodeDir := filepath.Join(genDir, shortName)

	for _, sub := range []string{
		"cra/netplan",
		"cra/certs",
		"cra/systemd-network/10-netplan-hbn.network.d",
		"netplan",
		"systemd-network/10-netplan-hbn.network.d",
	} {
		if err := os.MkdirAll(filepath.Join(nodeDir, sub), 0o755); err != nil {
			return err
		}
	}

	Logf("Generating configs for %s (VTEP=%s)", shortName, node.VtepIP)

	// Static files
	if err := os.WriteFile(filepath.Join(nodeDir, "cra/interfaces"), []byte("eth1\neth2\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(nodeDir, "cra/flavour"), []byte(craFlavor()+"\n"), 0o644); err != nil {
		return err
	}
	// grout: the CRA runs as a nerdctl container in namespace "hbr"; its image
	// ref (baked into the node image's containerd store at build time) is read
	// by cra-start.sh from /etc/cra/image. The harness regenerates /etc/cra on
	// each deploy, so it must (re)write this file for the grout flavour.
	if isGrout() {
		if err := os.WriteFile(filepath.Join(nodeDir, "cra/image"), []byte("das-schiff-cra-grout:latest\n"), 0o644); err != nil {
			return err
		}
		// The FRR CRA gets its VTEP address from netplan, on a dummy device.
		// grout's CRA has no netplan and no dummy devices, so cra-start.sh
		// assigns the VTEP to a grout interface instead -- but only if the
		// harness tells it which address to use. Without this file the VTEP
		// simply never exists, and every `vxlan ... local <vtep>` the agent and
		// FRR dplane later program has no source address.
		if err := os.WriteFile(filepath.Join(nodeDir, "cra/vtep"), []byte(node.VtepIP+"\n"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(nodeDir, "cra/vtep-iface"), []byte(uplinkName(1)+"\n"), 0o644); err != nil {
			return err
		}
	}

	data := templateData{
		VtepIP:        node.VtepIP,
		NodeIPv4:      node.IPv4,
		NodeIPv6:      node.IPv6,
		Hostname:      node.Hostname,
		BridgeMAC:     node.BridgeMAC,
		MgmtBridgeMAC: node.MgmtBridgeMAC,
		ClusterVNI:    clusterVNI,
		ClusterRT:     clusterRT,
		MgmtVNI:       mgmtVNI,
		MgmtRT:        mgmtRT,
		ExportCIDRv4:  exportCIDRv4,
		ExportCIDRv6:  exportCIDRv6,
		UnderlayVMv4:  underlayVMv4,
		UnderlayVMv6:  underlayVMv6,
		Uplink1:       uplinkName(1),
		Uplink2:       uplinkName(2),
		EVPNSource:    evpnSource(node.VtepIP),
		// Only the opt-in routed KubeVirt lab needs the CRA to export routed
		// workload host routes into the (v4/v6) underlay. Enabling it for the
		// default base/intent runs negotiates a fabric-facing IPv6 unicast AF on
		// the EVPN VTEP that breaks the stretched tenant (m2m/c2m) datapath.
		RoutedUnderlay: EnvOr("E2E_KUBEVIRT", "") != "",
		KubeVIPPeer:    nodeHbnAddr(),
	}

	// Render templates
	templates := []struct {
		src string
		dst string
	}{
		{"base-config.yaml.tpl", "cra/base-config.yaml"},
		{"frr.conf.tpl", "cra/frr.conf"},
		{"netplan/10-base.yaml.tpl", "cra/netplan/10-base.yaml"},
	}
	for _, t := range templates {
		if err := renderTemplate(filepath.Join(tplDir, t.src), filepath.Join(nodeDir, t.dst), data); err != nil {
			return fmt.Errorf("rendering %s for %s: %w", t.src, shortName, err)
		}
	}

	// CRA-side systemd-networkd drop-in: routes for node IPs via hbn.
	// FRR only: the FRR sidecar is reached over the `hbn` veth. The grout flavour
	// uses a dedicated `cra-mgmt` veth (created by cra-setup-network.sh) for the
	// agent<->sidecar path and its `hbn` is a DPDK data net_tap, so these
	// hbn-based mgmt drop-ins/netplan do not apply.
	if !isGrout() {
		hbnRoutes := fmt.Sprintf(`[Route]
Destination=%s/32
Gateway=fd00:7:caa5::1

[Route]
Destination=%s/128
Gateway=fd00:7:caa5::1
`, node.IPv4, node.IPv6)
		if err := os.WriteFile(
			filepath.Join(nodeDir, "cra/systemd-network/10-netplan-hbn.network.d/systemd-hbn-routes.conf"),
			[]byte(hbnRoutes), 0o644,
		); err != nil {
			return err
		}

		// Host-side systemd-networkd drop-in: IPv4 default via IPv6 next-hop (cross-family / RFC 5549).
		// Netplan can't express cross-family routes, so we add the IPv4 default via a networkd drop-in.
		hostHbnRoutes := fmt.Sprintf(`[Route]
Destination=0.0.0.0/0
Gateway=fd00:7:caa5::
Source=%s
`, node.IPv4)
		if err := os.WriteFile(
			filepath.Join(nodeDir, "systemd-network/10-netplan-hbn.network.d/ipv4-default-route.conf"),
			[]byte(hostHbnRoutes), 0o644,
		); err != nil {
			return err
		}
	}

	// Node identity env file
	identity := fmt.Sprintf("NODE_IPV4=%s\nNODE_IPV6=%s\nVTEP_IP=%s\nNODE_HOSTNAME=%s\n",
		node.IPv4, node.IPv6, node.VtepIP, node.Hostname)
	if err := os.WriteFile(filepath.Join(nodeDir, "node-identity.env"), []byte(identity), 0o644); err != nil {
		return err
	}

	// grout: the host-side `hbn` is a grout net_tap that only appears once grout
	// is up (moved to the host by cra-start.sh), so there is no boot-time netplan
	// for it -- the harness configures it after the CRA has started instead.
	//
	// The node still needs the very same thing the FRR flavour gets from netplan:
	// its own identity addresses and a default route into the cluster VRF.
	// Without them the node has no route off-box at all, and kubeadm dies pulling
	// images because it cannot even reach the NAT64 resolver. The link itself
	// cannot reuse fd00:7:caa5::/127 the way the FRR flavour does -- on grout that
	// prefix is already taken by the `cra-mgmt` control-plane veth -- so the data
	// trunk gets its own transfer net, in both families to keep the IPv4 default
	// route from having to cross families.
	if isGrout() {
		groutInit := fmt.Sprintf(`# Extra node-scoped grcli lines, applied by cra-start.sh.
# Transfer net towards the host on the `+"`hbn`"+` data trunk, plus host routes for
# the node's own addresses, so the CRA can route the node's traffic.
address add %s iface hbn
address add %s iface hbn
# The address Calico's BGP peer points at. The FRR flavour gets it from the CRA
# netplan as a lo_calico dummy in the cluster VRF; on grout the CRA has no
# netplan, so it is configured here. Without it calico-node never establishes
# its session, no pod prefix is ever advertised, and pod traffic between nodes
# is silently blackholed.
address add %s/128 iface hbn
address add %s/32 iface hbn
route add %s/32 via %s vrf cluster
route add %s/128 via %s vrf cluster
`, groutHbnCRAv6, groutHbnCRAv4, calicoPeerV6Addr, calicoPeerV4Addr,
			node.IPv4, groutHbnNodeV4Addr, node.IPv6, groutHbnNodeV6Addr)
		if err := os.WriteFile(filepath.Join(nodeDir, "cra/grout-base.init"), []byte(groutInit), 0o644); err != nil {
			return err
		}
		hbnNetplan := fmt.Sprintf(`# HBN trunk interface (grout) — parent for operator-managed VLANs.
network:
  version: 2
  ethernets:
    hbn:
      addresses:
        # The transfer address is deprecated (lifetime 0) on purpose. It is the
        # same on every node, so it must never be picked as a source address:
        # iptables MASQUERADE picks the source with the longest common prefix to
        # the destination, which for the fd10:: pod and service prefixes is this
        # fd00:: transfer address rather than the node's own fdcb:: address. The
        # remote CRA then routes the reply to *its own* node and every
        # masqueraded connection -- every kube-proxy ClusterIP session, admission
        # webhooks included -- hangs. Deprecating it leaves it usable as a
        # next-hop while excluding it from source selection.
        - "%s":
            lifetime: 0
        - %s
        - %s/32
        - %s/128
      # The trunk needs a link-local address, even though nothing addresses it
      # by one: Linux sources IPv6 neighbour *probes* from the interface's
      # link-local address and silently drops the probe when there is none. The
      # node would resolve the CRA once, from the pending packet's source, and
      # then fail every reachability confirmation from then on -- the entry goes
      # to FAILED without a single solicitation ever reaching the wire, and the
      # node loses its default route minutes after the fabric came up.
      link-local: [ipv6]
      critical: true
      routes:
        - to: "::/0"
          via: "%s"
          from: %s
        - to: "0.0.0.0/0"
          via: "%s"
          from: %s
`, groutHbnNodeV6, groutHbnNodeV4, node.IPv4, node.IPv6,
			groutHbnCRAv6Addr, node.IPv6, groutHbnCRAv4Addr, node.IPv4)
		return os.WriteFile(filepath.Join(nodeDir, "netplan/10-hbn.yaml"), []byte(hbnNetplan), 0o600)
	}

	// Host-side netplan config for hbn trunk interface.
	// critical: true → KeepConfiguration=true in systemd-networkd (hitless apply).
	// Addresses are per-node; routes go via CRA's link-local on the other end of the veth.
	hbnNetplan := fmt.Sprintf(`# HBN trunk interface — parent for operator-managed VLANs.
network:
  version: 2
  ethernets:
    hbn:
      addresses:
        - fd00:7:caa5::1/127
        - %s/32
        - %s/128
      link-local: []
      critical: true
      routes:
        - to: "::/0"
          via: "fd00:7:caa5::"
          from: %s
`, node.IPv4, node.IPv6, node.IPv6)
	return os.WriteFile(filepath.Join(nodeDir, "netplan/10-hbn.yaml"), []byte(hbnNetplan), 0o600)
}

// uplinkName returns the CRA-side interface name of fabric uplink n.
func uplinkName(n int) string {
	if isGrout() {
		return fmt.Sprintf("uplink%d", n)
	}
	return fmt.Sprintf("eth%d", n)
}

// The transfer net between the node and the CRA on the grout `hbn` data trunk.
// The FRR flavour reuses fd00:7:caa5::/127 for this, but on grout that prefix
// belongs to the `cra-mgmt` control-plane veth, so the data trunk needs its own.
const (
	// craTrunkName is the node<->CRA trunk interface, matching
	// trunkInterfaceName in the rendered CRA base config.
	craTrunkName = "hbn"

	groutHbnCRAv6Addr  = "fd00:7:caa5:2::"
	groutHbnNodeV6Addr = "fd00:7:caa5:2::1"
	groutHbnCRAv4Addr  = "169.254.2.0"
	groutHbnNodeV4Addr = "169.254.2.1"

	// calicoPeerV6Addr / calicoPeerV4Addr are the CRA-side addresses calico-node
	// peers with, matching `peerIP` in e2e/calico/bgppeer-hbn-{ipv6,ipv4}.yaml
	// and the FRR `updateSource`.
	calicoPeerV6Addr = "fd00:7:caa5:1::"
	calicoPeerV4Addr = "169.254.100.100"

	groutHbnCRAv6  = groutHbnCRAv6Addr + "/127"
	groutHbnNodeV6 = groutHbnNodeV6Addr + "/127"
	groutHbnCRAv4  = groutHbnCRAv4Addr + "/31"
	groutHbnNodeV4 = groutHbnNodeV4Addr + "/31"
)

// evpnSource returns the `update-source` for the EVPN route-reflector sessions.
func evpnSource(vtepIP string) string {
	if isGrout() {
		return vtepIP
	}
	return "dum.underlay"
}

type templateData struct {
	VtepIP        string
	NodeIPv4      string
	NodeIPv6      string
	Hostname      string
	BridgeMAC     string
	MgmtBridgeMAC string
	ClusterVNI    int
	ClusterRT     string
	MgmtVNI       int
	MgmtRT        string
	ExportCIDRv4  string
	ExportCIDRv6  string
	UnderlayVMv4  string
	UnderlayVMv6  string
	// Uplink1/Uplink2 are the CRA-side names of the two fabric uplinks that
	// carry the BGP-unnumbered underlay sessions. They differ per flavour: the
	// FRR CRA is an nspawn container whose fabric veths land as eth1/eth2,
	// while the grout CRA has no kernel fabric NICs at all -- the uplinks are
	// grout ports (uplink1/uplink2) that dplane_grout syncs into zebra under
	// those names. Hardcoding eth1/eth2 left grout's FRR peering with
	// interfaces that do not exist, so the node sessions stayed Idle.
	Uplink1 string
	Uplink2 string
	// EVPNSource is the `update-source` of the iBGP EVPN sessions to the route
	// reflectors. The FRR CRA carries the VTEP on a netplan dummy device and can
	// name it directly. The grout CRA has no kernel devices in zebra at all --
	// dplane_grout disables the kernel namespace -- so the VTEP lives on a grout
	// port and the session must be sourced from the address itself.
	EVPNSource string
	// RoutedUnderlay gates the routed-CNI underlay export on the CRA (the extra
	// IPv6 unicast address-family and `redistribute kernel` of the routed VM/pod
	// pool). It is only needed for the opt-in routed KubeVirt datapath lab; the
	// default base/intent runs have no routed workloads and must NOT advertise
	// (and thereby negotiate) that fabric-facing IPv6 unicast AF, which otherwise
	// disturbs the stretched tenant EVPN datapath.
	RoutedUnderlay bool
	// KubeVIPPeer is the node-side address of the node<->CRA trunk, where
	// kube-vip sources its BGP session from. It is flavour-dependent: the grout
	// data trunk has its own transfer net because the FRR one is taken by the
	// grout CRA's `cra-mgmt` veth.
	KubeVIPPeer string
}

// renderTemplate renders a Go template file with {{ .Var }} placeholders.
func renderTemplate(srcPath, dstPath string, data templateData) error {
	content, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}

	tmpl, err := template.New(filepath.Base(srcPath)).Parse(string(content))
	if err != nil {
		return fmt.Errorf("parsing template %s: %w", srcPath, err)
	}

	f, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}
