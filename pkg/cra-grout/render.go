package cra

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"unicode"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
)

// bridgeName derives the grout L2 bridge domain name for a Layer2 VNI. Kept
// short to stay within grout's interface-name limits.
func bridgeName(vni uint32) string {
	return fmt.Sprintf("br%d", vni)
}

// RenderGrcli renders the desired grout fast-path state for a node as a grcli
// batch. It creates the cluster/fabric/local VRFs, the EVPN L3VNI/L2VNI VXLAN
// interfaces, the L2 bridge domains with their attached ports (both the shared
// trunk VLAN sub-interface and any workload-CNI access ports), and the routed
// workload ports (net_tap for the veth transport, net_vhost for vhostuser) with
// their on-link gateway addresses and host routes.
func RenderGrcli(baseConfig *config.BaseConfig, spec *v1alpha1.NodeNetworkConfigSpec) (string, error) {
	b := NewBatch()
	vtep := baseConfig.VTEPLoopbackIP

	clusterVRFName := baseConfig.ClusterVRF.Name

	// VRFs first: ports, bridges and VXLANs reference them.
	b.Commentf("VRFs")
	if spec.ClusterVRF != nil && clusterVRFName != "" {
		b.AddVRF(clusterVRFName)
	}
	for _, name := range sortedFabricVRFNames(spec.FabricVRFs) {
		if name == baseConfig.ManagementVRF.Name {
			continue
		}
		b.AddVRF(name)
	}
	for _, name := range sortedVRFNames(spec.LocalVRFs) {
		b.AddVRF(name)
	}

	// EVPN L3VNI VXLAN interfaces for fabric VRFs carrying a VNI.
	for _, name := range sortedFabricVRFNames(spec.FabricVRFs) {
		if name == baseConfig.ManagementVRF.Name {
			continue
		}
		vrf := spec.FabricVRFs[name]
		if vrf.VNI != 0 {
			b.Commentf("L3VNI for VRF %s", name)
			b.AddL3VNI(vrf.VNI, vtep, name, DefaultOverlayMTU)
		}
	}

	// EVPN L2VNI bridges + VXLANs, and their L2-attached workload-CNI ports.
	if err := renderLayer2s(b, spec, vtep, baseConfig.TrunkInterfaceName); err != nil {
		return "", err
	}

	// Routed workload ports per VRF, plus the global (no-VRF) underlay ports.
	if err := renderWorkloadPorts(b, spec, clusterVRFName); err != nil {
		return "", err
	}

	return b.String(), nil
}

func renderLayer2s(b *Batch, spec *v1alpha1.NodeNetworkConfigSpec, vtep, trunk string) error {
	// A workload-CNI trunk port carries several L2 domains, so it appears once
	// per domain in AttachedPorts but must be created exactly once -- and before
	// the first sub-interface that names it as a parent. Emit all of them up
	// front; trunkPorts then records which ports are trunks so the per-domain
	// loop below renders a sub-interface instead of a bridge-slave port.
	trunkPorts, err := renderTrunkPorts(b, spec.Layer2s)
	if err != nil {
		return err
	}

	for _, key := range sortedLayer2Keys(spec.Layer2s) {
		l2 := spec.Layer2s[key]
		br := bridgeName(l2.VNI)
		irbVRF := ""
		if l2.IRB != nil {
			irbVRF = l2.IRB.VRF
		}
		b.Commentf("L2VNI %d (bridge %s)", l2.VNI, br)
		b.AddL2Bridge(br, irbVRF, l2.MTU)
		// Attach the L2VNI VXLAN to the bridge domain before addressing the IRB
		// SVI, so the domain is established when the anycast-gateway address is
		// added. The IRB gateway IP lives on the L2VNI bridge SVI (bound to the
		// tenant VRF via AddL2Bridge) -- the L3VNI is a pure L3 transit VNI and
		// carries no address.
		b.AddL2VNI(l2.VNI, vtep, br, l2.MTU)
		if l2.IRB != nil {
			for _, gw := range l2.IRB.IPAddresses {
				b.AddAddress(gw, br)
			}
		}

		// Map the L2's VLAN on the shared fabric trunk into this bridge domain,
		// so workloads attached via macvlan on the host-side trunk netdev (which
		// tag with l2.VLAN) are bridged into the L2VNI. Only emitted when the
		// node has a trunk configured and the L2 carries a VLAN; the trunk port
		// itself stays in VRF mode so grout performs VLAN demux into this
		// sub-interface. Access ports attached directly via the workload CNI
		// (AttachedPorts, below) are an independent path onto the same bridge.
		if trunk != "" && l2.VLAN != 0 {
			b.AddTrunkVlanToBridge(trunk, l2.VLAN, br, 0)
		}

		for i := range l2.AttachedPorts {
			ap := &l2.AttachedPorts[i]
			if err := renderAttachedPort(b, ap, br, l2.MTU, trunkPorts); err != nil {
				return fmt.Errorf("L2VNI %d attached port %q: %w", l2.VNI, ap.Interface, err)
			}
		}
	}
	return nil
}

// renderTrunkPorts creates every workload-CNI port that carries a VLAN tag,
// unbound (VRF mode) so grout demuxes its tags into the sub-interfaces that
// renderAttachedPort hangs off it. It returns the set of such ports.
//
// The port is sized for the largest MTU any of its members asks for. All members
// carry the same requested MTU today (they come from one attachment, so one CNI
// configuration), but taking the maximum keeps a port from being silently capped
// below a domain it carries if that ever stops holding.
func renderTrunkPorts(b *Batch, layer2s map[string]v1alpha1.Layer2) (map[string]bool, error) {
	mtus := map[string]uint16{}
	ports := map[string]*v1alpha1.AttachedPort{}
	for _, key := range sortedLayer2Keys(layer2s) {
		l2 := layer2s[key]
		for i := range l2.AttachedPorts {
			ap := &l2.AttachedPorts[i]
			if ap.VLAN == 0 {
				continue
			}
			if _, seen := ports[ap.Interface]; !seen {
				ports[ap.Interface] = ap
			}
			if mtu := attachedPortMTU(ap); mtu > mtus[ap.Interface] {
				mtus[ap.Interface] = mtu
			}
		}
	}
	if len(ports) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)

	trunks := make(map[string]bool, len(names))
	b.Commentf("workload-CNI trunk ports")
	for _, name := range names {
		ap := ports[name]
		if err := validateAttachedPortTransport(ap); err != nil {
			return nil, fmt.Errorf("trunk port %q: %w", name, err)
		}
		b.AddTrunkPort(name, ap.Transport, ap.SocketPath, groutIsClient(ap.SocketMode), mtus[name])
		trunks[name] = true
	}
	return trunks, nil
}

// renderAttachedPort wires one workload-CNI attachment into an L2 bridge domain.
//
// An access port (VLAN 0) is the port itself, bound to the domain. A trunk
// member (VLAN set) is a sub-interface of a port renderTrunkPorts already
// created: the port stays unbound so grout demuxes, and it is the sub-interface
// that joins the domain.
func renderAttachedPort(b *Batch, ap *v1alpha1.AttachedPort, bridge string, bridgeMTU uint16, trunkPorts map[string]bool) error {
	if err := validateAttachedPortTransport(ap); err != nil {
		return err
	}
	if ap.VLAN != 0 {
		if !trunkPorts[ap.Interface] {
			return fmt.Errorf("trunk member on vlan %d has no trunk port", ap.VLAN)
		}
		// Clamped to the domain: a trunk is admitted when *one* member's Layer2
		// carries the requested MTU (workloadcni.checkLayer2MTU), so the smaller
		// domains on the same port legitimately see a request larger than they
		// can carry. Sizing the sub-interface above its own bridge would put an
		// MTU and a domain that disagree on one grcli line.
		b.AddTrunkVlanToBridge(ap.Interface, ap.VLAN, bridge, minMTU(attachedPortMTU(ap), bridgeMTU))
		return nil
	}

	if ap.Transport == v1alpha1.PortTransportVhostUser {
		b.AddVhostPortToBridge(ap.Interface, ap.SocketPath, groutIsClient(ap.SocketMode), bridge, attachedPortMTU(ap))
		return nil
	}
	b.AddTapPortToBridge(ap.Interface, bridge, attachedPortMTU(ap))
	return nil
}

// minMTU returns the smaller of two MTUs, ignoring an unset (zero) bound.
func minMTU(want, bound uint16) uint16 {
	if bound != 0 && bound < want {
		return bound
	}
	return want
}

// validateAttachedPortTransport rejects a transport this datapath cannot render,
// and a vhost-user attachment whose socket path is missing or is not a single
// plain token. The path is re-checked here for the same reason the routed path
// re-checks it: a batch line is split on whitespace and run as a command, so a
// value carrying a separator would stop being an argument and start being one.
func validateAttachedPortTransport(ap *v1alpha1.AttachedPort) error {
	switch ap.Transport {
	case v1alpha1.PortTransportVhostUser:
		if ap.SocketPath == "" {
			return fmt.Errorf("vhostuser transport requires a socket path")
		}
		if err := validateDevargsValue(ap.SocketPath); err != nil {
			return fmt.Errorf("vhostuser socket path: %w", err)
		}
	case v1alpha1.PortTransportVeth, "":
	default:
		return fmt.Errorf("unsupported transport %q", ap.Transport)
	}
	return nil
}

// attachedPortMTU is the MTU the attachment requested, or the shared default
// when it asked for none. It is applied to the port and to every sub-interface
// derived from it, so the fast path never caps a workload below the size its own
// netdev was configured with.
func attachedPortMTU(ap *v1alpha1.AttachedPort) uint16 {
	if ap.MTU != 0 {
		return ap.MTU
	}
	return workloadcni.DefaultPortMTU
}

// workloadPortMTU is attachedPortMTU for a routed port.
func workloadPortMTU(p *v1alpha1.WorkloadPort) uint16 {
	if p.MTU != 0 {
		return p.MTU
	}
	return workloadcni.DefaultPortMTU
}

func renderWorkloadPorts(b *Batch, spec *v1alpha1.NodeNetworkConfigSpec, clusterVRFName string) error {
	if spec.ClusterVRF != nil {
		if err := renderVRFWorkloadPorts(b, spec.ClusterVRF.WorkloadPorts, clusterVRFName); err != nil {
			return err
		}
	}
	for _, name := range sortedFabricVRFNames(spec.FabricVRFs) {
		vrf := spec.FabricVRFs[name]
		if err := renderVRFWorkloadPorts(b, vrf.WorkloadPorts, name); err != nil {
			return err
		}
	}
	for _, name := range sortedVRFNames(spec.LocalVRFs) {
		vrf := spec.LocalVRFs[name]
		if err := renderVRFWorkloadPorts(b, vrf.WorkloadPorts, name); err != nil {
			return err
		}
	}
	// Ports requested without a target VRF land in grout's default table, which
	// is addressed by the empty VRF name.
	return renderVRFWorkloadPorts(b, spec.GlobalWorkloadPorts, "")
}

func renderVRFWorkloadPorts(b *Batch, ports []v1alpha1.WorkloadPort, vrf string) error {
	for i := range ports {
		p := &ports[i]
		b.Commentf("workload port %s (vrf %s)", p.Interface, vrfLabel(vrf))
		switch p.Transport {
		case v1alpha1.PortTransportVhostUser:
			// The path is already validated where it enters the cluster
			// (workloadcni.validateSocketPath); it is re-checked here because a
			// batch line is split on whitespace and run as a command, so a value
			// carrying a separator would stop being an argument and start being
			// one. A renderer that is one bad string away from executing an
			// attacker's grcli command should not depend on a caller it does not
			// control.
			if err := validateDevargsValue(p.SocketPath); err != nil {
				return fmt.Errorf("workload port %q: vhostuser socket path: %w", p.Interface, err)
			}
			b.AddVhostPort(p.Interface, p.SocketPath, groutIsClient(p.SocketMode), vrf, workloadPortMTU(p))
		case v1alpha1.PortTransportVeth, "":
			b.AddTapPort(p.Interface, vrf, workloadPortMTU(p))
		default:
			return fmt.Errorf("workload port %q: unsupported transport %q", p.Interface, p.Transport)
		}

		// The IPv4 gateway is deliberately never programmed on this datapath,
		// even if a producer still sets it. grout keeps one node-global IPv4
		// address table with no per-interface scope for link-local space, so the
		// shared 169.254.1.1/32 every routed port asks for can exist on exactly
		// one port; the next one is rejected with EADDRINUSE. That is not a
		// contained failure: a rejected grcli line aborts the rest of the batch,
		// the agent marks the NodeNetworkConfig invalid, and because a revision
		// is named after a hash of its content, regenerating it reproduces the
		// same invalid revision — config deployment stops cluster-wide.
		//
		// IPv6 link-local is scoped per interface, so fe80::1/128 is accepted on
		// every port. The workload therefore reaches the fabric over IPv4 with
		// that address as its next-hop (RTA_VIA, installed pod-side by the CNI),
		// which needs no IPv4 address here at all and lifts the one-routed-
		// attachment-per-node limit.
		if p.GatewayV6 != "" {
			b.AddAddress(p.GatewayV6, p.Interface)
		}
		if err := renderWorkloadHostRoutes(b, p, vrf); err != nil {
			return err
		}
	}
	return nil
}

// renderWorkloadHostRoutes installs a routed port's host routes, resolving the
// IPv4 ones over an IPv6 nexthop where it can.
//
// It has to. This datapath carries no IPv4 address on a routed workload port
// (see the gateway comment above), and grout picks the source address for an
// ARP request from the outgoing interface: arp_output_request calls
// addr4_get_preferred() for the port and drops the packet when it comes back
// empty. An IPv4 nexthop on such a port therefore never leaves state=failed and
// nothing from the fabric can reach the workload, however correct the route is.
//
// The IPv6 host route on the same port gives a nexthop that *can* resolve --
// neighbour solicitation has fe80::1 to source from -- and grout's forwarding
// path reads the resolved MAC off the nexthop without comparing address
// families, so an IPv4 prefix may use it. That is RFC 5549 in the CRA-to-
// workload direction, mirroring the RTA_VIA default route the CNI installs
// workload-side for the other one.
//
// A single-stack IPv4 port has no such nexthop and keeps the on-link IPv4 one:
// it cannot resolve, but it is the honest rendering of an attachment this
// datapath cannot carry, and it keeps the batch valid so the rest of the node's
// configuration still applies.
func renderWorkloadHostRoutes(b *Batch, p *v1alpha1.WorkloadPort, vrf string) error {
	// The IPv6 routes are added first so their nexthops exist by the time an
	// IPv4 route asks to borrow one. Ordering within a batch is significant:
	// grcli runs the lines in sequence and `route add via id` requires the id.
	var v6NexthopID uint32
	var haveV6Nexthop bool
	for _, hr := range p.HostRoutes {
		if !isIPv6HostRoute(hr) {
			continue
		}
		id, err := b.AddOnLinkHostRoute(hr, p.Interface, vrf)
		if err != nil {
			return fmt.Errorf("workload port %q host route: %w", p.Interface, err)
		}
		// The first in the configured order wins, so the choice is stable
		// across renders and the batch stays byte-identical for a given spec.
		if !haveV6Nexthop {
			v6NexthopID, haveV6Nexthop = id, true
		}
	}
	for _, hr := range p.HostRoutes {
		if isIPv6HostRoute(hr) {
			continue
		}
		if haveV6Nexthop {
			if err := b.AddHostRouteVia(hr, v6NexthopID, vrf); err != nil {
				return fmt.Errorf("workload port %q host route: %w", p.Interface, err)
			}
			continue
		}
		if _, err := b.AddOnLinkHostRoute(hr, p.Interface, vrf); err != nil {
			return fmt.Errorf("workload port %q host route: %w", p.Interface, err)
		}
	}
	return nil
}

// isIPv6HostRoute reports whether a host route names an IPv6 destination. An
// unparseable value is left to AddOnLinkHostRoute, which reports it properly.
func isIPv6HostRoute(hostCIDR string) bool {
	addr, err := hostAddr(hostCIDR)
	if err != nil {
		return false
	}
	ip := net.ParseIP(addr)
	return ip != nil && ip.To4() == nil
}

// vrfLabel renders the VRF name for a comment, naming the default table
// explicitly so a global (no-VRF) port is not mistaken for a rendering bug.
func vrfLabel(vrf string) string {
	if vrf == "" {
		return "default"
	}
	return vrf
}

// groutIsClient maps the workload-perspective vhost-user socket mode onto
// grout's net_vhost client flag. The two ends of a vhost-user socket must take
// opposite roles (like VSR's invertSocketMode): when the workload owns the
// socket ("server"), grout must connect as the client (client=1); when the
// workload connects ("client") or the mode is unset, grout owns the socket
// (server, client=0). grout net_vhost client=1 => grout connects to an existing
// socket; client=0 => grout creates and listens on it.
func groutIsClient(socketMode string) bool {
	return socketMode == "server"
}

func sortedLayer2Keys(m map[string]v1alpha1.Layer2) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedFabricVRFNames(m map[string]v1alpha1.FabricVRF) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedVRFNames(m map[string]v1alpha1.VRF) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validateDevargsValue rejects a value that cannot be safely placed in a grcli
// devargs string: the batch is executed line by line and split on whitespace,
// and commas separate devargs keys.
func validateDevargsValue(v string) error {
	if v == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.ContainsFunc(v, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == ','
	}) {
		return fmt.Errorf("%q contains whitespace, a control character or a comma", v)
	}
	return nil
}
