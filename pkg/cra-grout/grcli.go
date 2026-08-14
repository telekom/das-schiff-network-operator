package cra

import (
	"fmt"
	"hash/fnv"
	"net"
	"strings"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
)

// Batch accumulates grcli commands describing the desired grout state. Commands
// use the grammar validated live against grout (see files/grout-poc): each is a
// single line applied in order by `grcli -ef`.
type Batch struct {
	lines []string
}

// NewBatch returns an empty grcli batch.
func NewBatch() *Batch {
	return &Batch{}
}

// String renders the batch as a newline-terminated grcli script.
func (b *Batch) String() string {
	if len(b.lines) == 0 {
		return ""
	}
	return strings.Join(b.lines, "\n") + "\n"
}

// Commentf appends a `#`-prefixed comment line (grcli ignores it).
func (b *Batch) Commentf(format string, args ...any) {
	b.lines = append(b.lines, "# "+fmt.Sprintf(format, args...))
}

func (b *Batch) addf(format string, args ...any) {
	b.lines = append(b.lines, fmt.Sprintf(format, args...))
}

// DefaultOverlayMTU is the MTU used for overlay interfaces when the intent does
// not carry one. It matches nl.DefaultMtu, which the kernel datapath uses for
// the same interfaces, and is deliberately not left to grout: grout derives a
// VXLAN interface's default MTU from a 1500-byte underlay (1450), so an overlay
// left at the default silently blackholes anything the node sends at its own
// 1500-byte MTU -- grout emits no ICMP "packet too big", so PMTU discovery
// never recovers and the only symptom is a connection that hangs.
const DefaultOverlayMTU = 9000

func overlayMTU(mtu uint16) uint16 {
	if mtu == 0 {
		return DefaultOverlayMTU
	}
	return mtu
}

// FIB sizing for operator-managed VRFs. grout allocates each VRF's IPv4/IPv6
// FIB up front from the DPDK heap, and its built-in defaults (65536 routes with
// 262144 IPv6 tbl8 groups) are sized for a router with a full table: allocating
// them for every tenant VRF exhausts the heap, the FIB is left unallocated and
// every subsequent `address add` on an interface in that VRF fails with ENONET.
// These values match the base VRFs created by the CRA's start script.
const (
	DefaultVRFRib4Routes = 8192
	DefaultVRFFib4Tbl8   = 64
	DefaultVRFRib6Routes = 8192
	DefaultVRFFib6Tbl8   = 1024
)

// AddVRF creates a grout VRF domain with the operator's FIB sizing:
// `interface add vrf <name> rib4-routes ... fib6-tbl8 ...`.
func (b *Batch) AddVRF(name string) {
	b.addf("interface add vrf %s rib4-routes %d fib4-tbl8 %d rib6-routes %d fib6-tbl8 %d",
		name, DefaultVRFRib4Routes, DefaultVRFFib4Tbl8, DefaultVRFRib6Routes, DefaultVRFFib6Tbl8)
}

// vdevName derives the DPDK vdev name for a port from the grout interface name,
// e.g. ("net_tap", "cra0123abcd") -> "net_tap_cra0123abcd".
//
// The name is derived from the port rather than from the port's position in the
// batch, and that distinction is load-bearing. The batch is a full desired-state
// replay, so a positional index is only stable while the set of ports is: delete
// one port and every later port shifts down one index. Because grout tolerates a
// re-add of an existing port as EEXIST, the shift is invisible until a NEW port
// is rendered -- at which point it is handed an index still held by a live vdev,
// and DPDK rejects the duplicate name with EEXIST, permanently. The port names
// are already unique and stable (a hash of container id + ifname), so deriving
// from them makes the batch genuinely idempotent.
//
// DPDK binds a vdev to its PMD by prefix-matching the driver name, so any suffix
// after "net_tap"/"net_vhost" is legal; the numeric convention is not a
// requirement (rte_bus_vdev vdev_probe_all_drivers / vdev.c).
func vdevName(driver, port string) string {
	return driver + "_" + port
}

// tapDevargs returns the net_tap devargs for a port, with a kernel-visible tap
// named after the port but deliberately distinct from it (e.g.
// "net_tap_cra0123,iface=cra0123_dp"). grout creates the tap in its own (CRA)
// netns; the agent then moves the netdev into the workload netns.
func tapDevargs(port string) string {
	return fmt.Sprintf("%s,iface=%s", vdevName("net_tap", port), workloadcni.TapIfaceName(port))
}

// vhostDevargs returns the net_vhost devargs for a vhost-user socket. client is
// true when grout should connect to the socket as the vhost-user client (the
// workload owns the socket), false when grout owns it (server).
func vhostDevargs(port, socketPath string, client bool) string {
	mode := 0
	if client {
		mode = 1
	}
	return fmt.Sprintf("%s,iface=%s,client=%d", vdevName("net_vhost", port), socketPath, mode)
}

// AddTapPort adds a DPDK net_tap port bound into vrf (empty vrf => default).
// The tap's kernel netdev name is derived from the port rather than passed in, so
// it cannot accidentally be given the interface's own name (see
// workloadcni.TapIfaceName).
func (b *Batch) AddTapPort(name, vrf string, mtu uint16) {
	b.addPort(name, tapDevargs(name), mtu, "vrf", vrf)
}

// AddVhostPort adds a DPDK net_vhost port bound into vrf (empty vrf => default).
func (b *Batch) AddVhostPort(name, socketPath string, client bool, vrf string, mtu uint16) {
	b.addPort(name, vhostDevargs(name, socketPath, client), mtu, "vrf", vrf)
}

// AddTapPortToBridge adds a DPDK net_tap port enslaved to an L2 bridge domain.
func (b *Batch) AddTapPortToBridge(name, bridge string, mtu uint16) {
	b.addPort(name, tapDevargs(name), mtu, "domain", bridge)
}

// AddVhostPortToBridge adds a DPDK net_vhost port enslaved to an L2 bridge domain.
func (b *Batch) AddVhostPortToBridge(name, socketPath string, client bool, bridge string, mtu uint16) {
	b.addPort(name, vhostDevargs(name, socketPath, client), mtu, "domain", bridge)
}

// AddTrunkPort adds a DPDK port that carries several tagged L2 domains. It is
// created with no binding at all, which leaves it in grout's VRF mode in the
// default VRF.
//
// That is not a detail of presentation: grout only looks a frame's VLAN id up
// against the receiving interface's sub-interfaces when that interface is in VRF
// mode (iface_input.c: `d->vlan_id != 0 && d->iface->mode == GR_IFACE_MODE_VRF`).
// Binding the port to a domain instead puts it in bridge mode, at which point
// every frame -- tagged or not -- is dispatched straight into that one domain
// and the sub-interfaces below never receive anything.
func (b *Batch) AddTrunkPort(name string, transport v1alpha1.PortTransport, socketPath string, client bool, mtu uint16) {
	devargs := tapDevargs(name)
	if transport == v1alpha1.PortTransportVhostUser {
		devargs = vhostDevargs(name, socketPath, client)
	}
	b.addPort(name, devargs, mtu, "vrf", "")
}

func (b *Batch) addPort(name, devargs string, mtu uint16, bindKind, bindName string) {
	line := fmt.Sprintf("interface add port %s devargs %s", name, devargs)
	// grout's own default is the DPDK device's, which for a net_tap or a
	// net_vhost is 1500 regardless of what the attachment asked for. Size the
	// port explicitly so the fast path drops nothing the workload's own netdev
	// was willing to send.
	if mtu != 0 {
		line += fmt.Sprintf(" mtu %d", mtu)
	}
	if bindName != "" {
		line += fmt.Sprintf(" %s %s", bindKind, bindName)
	}
	b.addf("%s", line)
}

// AddAddress assigns an on-link address (CIDR) to an interface.
func (b *Batch) AddAddress(cidr, iface string) {
	b.addf("address add %s iface %s", cidr, iface)
}

// AddL3VNI creates an EVPN symmetric-IRB L3VNI VXLAN interface mapped to vrf:
// `interface add vxlan l3vni<vni> vni <vni> local <vtep> vrf <vrf> mtu <mtu>`.
func (b *Batch) AddL3VNI(vni uint32, vtep, vrf string, mtu uint16) {
	b.addf("interface add vxlan l3vni%d vni %d local %s vrf %s mtu %d", vni, vni, vtep, vrf, overlayMTU(mtu))
}

// AddL2Bridge creates an L2 bridge domain. When vrf is non-empty the bridge is
// an IRB SVI in that VRF: `interface add bridge br<vni> vrf <vrf>`.
func (b *Batch) AddL2Bridge(bridge, vrf string, mtu uint16) {
	if vrf != "" {
		b.addf("interface add bridge %s vrf %s mtu %d", bridge, vrf, overlayMTU(mtu))
		return
	}
	b.addf("interface add bridge %s mtu %d", bridge, overlayMTU(mtu))
}

// AddL2VNI creates an EVPN L2VNI VXLAN interface bound to an L2 bridge domain:
// `interface add vxlan l2vni<vni> vni <vni> local <vtep> domain <bridge> mtu <mtu>`.
func (b *Batch) AddL2VNI(vni uint32, vtep, bridge string, mtu uint16) {
	b.addf(
		"interface add vxlan l2vni%d vni %d local %s domain %s mtu %d",
		vni, vni, vtep, bridge, overlayMTU(mtu),
	)
}

// VlanIfaceName is the grout name of the VLAN sub-interface carrying vlan on
// parent. It matches the netdev name the kernel (cra-frr) and the VSR flavours
// use for the same member, so a trunk looks the same on every datapath.
func VlanIfaceName(parent string, vlan uint16) string {
	return fmt.Sprintf("%s.%d", parent, vlan)
}

// AddTrunkVlanToBridge maps a VLAN carried on a trunk port into an L2 bridge
// domain, so VLAN-tagged frames arriving on that port are bridged into the
// L2VNI. It renders a grout VLAN sub-interface enslaved to the bridge:
// `interface add vlan <trunk>.<vlan> parent <trunk> vlan_id <vlan> [mtu <mtu>] domain <bridge>`.
//
// It serves both trunks the CRA has: the shared fabric trunk, where workloads
// attach with macvlan on the host-side netdev and tag with <vlan>; and a
// workload-CNI trunk port, where the pod itself tags. Pairing a sub-interface
// whose vlan_id is the workload-side id with the bridge of a domain whose
// fabric-side id differs is what implements VLAN translation.
//
// The parent PORT must stay in grout's VRF mode (created with no `domain`, see
// AddTrunkPort), otherwise grout's iface_input skips the VLAN demux and the
// sub-interface never receives a frame.
//
// mtu 0 leaves the sub-interface at grout's default; for a workload member it
// is the MTU the attachment requested, so the sub-interface is sized like the
// port it hangs off rather than at whatever the platform defaults to.
func (b *Batch) AddTrunkVlanToBridge(trunk string, vlan uint16, bridge string, mtu uint16) {
	line := fmt.Sprintf("interface add vlan %s parent %s vlan_id %d",
		VlanIfaceName(trunk, vlan), trunk, vlan)
	if mtu != 0 {
		line += fmt.Sprintf(" mtu %d", mtu)
	}
	b.addf("%s domain %s", line, bridge)
}

// nexthopIDMask folds a hash into the low 31 bits of grout's uint32 nexthop id
// space, keeping clear of any sign-sensitive handling on the wire.
const nexthopIDMask = 0x7fffffff

// nexthopID derives the grout nexthop id for an on-link nexthop from the pair
// that defines it, rather than from a counter.
//
// A counter is only stable while the set of ports is, and the batch is a full
// desired-state replay: remove one workload port and every later nexthop shifts
// down an id. grout tolerates the re-add of an existing id as EEXIST, so the
// live nexthop keeps its OLD address and interface while the subsequent
// `route add ... via id N` binds the new route to it -- silently pointing a
// pod's host route at a different pod's interface. Deriving the id from
// (iface, address) makes the id mean the same thing on every replay, so the
// tolerated EEXIST is genuinely a no-op.
//
// The id space is grout's uint32; 0 is avoided because grout treats it as
// unset, and the value is folded into the low 31 bits to stay clear of any
// sign-sensitive handling on the wire.
func nexthopID(iface, addr string) uint32 {
	h := fnv.New32a()
	// The separator keeps ("ab", "c") and ("a", "bc") distinct.
	_, _ = h.Write([]byte(iface + "\x00" + addr))
	id := h.Sum32() & nexthopIDMask
	if id == 0 {
		id = 1
	}
	return id
}

// AddHostRouteVia installs a host route that resolves over an already-declared
// nexthop id instead of creating one of its own.
//
// It exists so an IPv4 prefix can be routed over an IPv6 nexthop (RFC 5549).
// grout's forwarding path takes the resolved MAC from the nexthop without
// consulting its address family (see ip_output), so the two need not match.
func (b *Batch) AddHostRouteVia(hostCIDR string, nhID uint32, vrf string) error {
	// Validated even though the nexthop is already declared, so that a
	// malformed route is rejected the same way whichever branch renders it.
	if _, err := hostAddr(hostCIDR); err != nil {
		return err
	}
	if vrf != "" {
		b.addf("route add %s via id %d vrf %s", hostCIDR, nhID, vrf)
	} else {
		b.addf("route add %s via id %d", hostCIDR, nhID)
	}
	return nil
}

// AddOnLinkHostRoute installs an on-link host route to a workload reachable
// directly on iface: it adds an L3 nexthop pointing at the workload address and
// adds the /32 or /128 route via that nexthop id in vrf. It returns the nexthop
// id, which is derived from the nexthop itself and so is stable across replays.
func (b *Batch) AddOnLinkHostRoute(hostCIDR, iface, vrf string) (uint32, error) {
	addr, err := hostAddr(hostCIDR)
	if err != nil {
		return 0, err
	}
	id := nexthopID(iface, addr)
	b.addf("nexthop add l3 iface %s id %d address %s", iface, id, addr)
	if vrf != "" {
		b.addf("route add %s via id %d vrf %s", hostCIDR, id, vrf)
	} else {
		b.addf("route add %s via id %d", hostCIDR, id)
	}
	return id, nil
}

// hostAddr strips the prefix length from a host CIDR ("10.0.0.5/32" ->
// "10.0.0.5"). A bare IP is accepted and returned unchanged.
func hostAddr(hostCIDR string) (string, error) {
	if !strings.Contains(hostCIDR, "/") {
		if net.ParseIP(hostCIDR) == nil {
			return "", fmt.Errorf("invalid host address %q", hostCIDR)
		}
		return hostCIDR, nil
	}
	ip, _, err := net.ParseCIDR(hostCIDR)
	if err != nil {
		return "", fmt.Errorf("invalid host CIDR %q: %w", hostCIDR, err)
	}
	return ip.String(), nil
}
