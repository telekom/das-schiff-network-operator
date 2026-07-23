package cra

import (
	"fmt"
	"net"
	"strings"
)

// Batch accumulates grcli commands describing the desired grout state. Commands
// use the grammar validated live against grout (see files/grout-poc): each is a
// single line applied in order by `grcli -ef`. Batch also allocates the unique
// numeric identifiers grout requires (nexthop ids and per-PMD device indices).
type Batch struct {
	lines      []string
	nextNHID   uint32
	tapIndex   int
	vhostIndex int
}

// NewBatch returns an empty grcli batch. Nexthop ids start at 1 (0 is reserved).
func NewBatch() *Batch {
	return &Batch{nextNHID: 1}
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

// AddVRF creates a grout VRF domain: `interface add vrf <name>`.
func (b *Batch) AddVRF(name string) {
	b.addf("interface add vrf %s", name)
}

// tapDevargs returns the next unique net_tap devargs for a kernel-visible tap
// named iface (e.g. "net_tap0,iface=cra0123"). grout creates the tap in its own
// (CRA) netns; the agent then moves the netdev into the workload netns.
func (b *Batch) tapDevargs(iface string) string {
	d := fmt.Sprintf("net_tap%d,iface=%s", b.tapIndex, iface)
	b.tapIndex++
	return d
}

// vhostDevargs returns the next unique net_vhost devargs for a vhost-user socket.
// client is true when grout should connect to the socket as the vhost-user
// client (the workload owns the socket), false when grout owns it (server).
func (b *Batch) vhostDevargs(socketPath string, client bool) string {
	mode := 0
	if client {
		mode = 1
	}
	d := fmt.Sprintf("net_vhost%d,iface=%s,client=%d", b.vhostIndex, socketPath, mode)
	b.vhostIndex++
	return d
}

// AddTapPort adds a DPDK net_tap port bound into vrf (empty vrf => default).
func (b *Batch) AddTapPort(name, iface, vrf string) {
	b.addPort(name, b.tapDevargs(iface), "vrf", vrf)
}

// AddVhostPort adds a DPDK net_vhost port bound into vrf (empty vrf => default).
func (b *Batch) AddVhostPort(name, socketPath string, client bool, vrf string) {
	b.addPort(name, b.vhostDevargs(socketPath, client), "vrf", vrf)
}

// AddTapPortToBridge adds a DPDK net_tap port enslaved to an L2 bridge domain.
func (b *Batch) AddTapPortToBridge(name, iface, bridge string) {
	b.addPort(name, b.tapDevargs(iface), "domain", bridge)
}

// AddVhostPortToBridge adds a DPDK net_vhost port enslaved to an L2 bridge domain.
func (b *Batch) AddVhostPortToBridge(name, socketPath string, client bool, bridge string) {
	b.addPort(name, b.vhostDevargs(socketPath, client), "domain", bridge)
}

func (b *Batch) addPort(name, devargs, bindKind, bindName string) {
	if bindName != "" {
		b.addf("interface add port %s devargs %s %s %s", name, devargs, bindKind, bindName)
		return
	}
	b.addf("interface add port %s devargs %s", name, devargs)
}

// AddAddress assigns an on-link address (CIDR) to an interface.
func (b *Batch) AddAddress(cidr, iface string) {
	b.addf("address add %s iface %s", cidr, iface)
}

// AddL3VNI creates an EVPN symmetric-IRB L3VNI VXLAN interface mapped to vrf:
// `interface add vxlan l3vni<vni> vni <vni> local <vtep> vrf <vrf>`.
func (b *Batch) AddL3VNI(vni uint32, vtep, vrf string) {
	b.addf("interface add vxlan l3vni%d vni %d local %s vrf %s", vni, vni, vtep, vrf)
}

// AddL2Bridge creates an L2 bridge domain. When vrf is non-empty the bridge is
// an IRB SVI in that VRF: `interface add bridge br<vni> vrf <vrf>`.
func (b *Batch) AddL2Bridge(bridge, vrf string) {
	if vrf != "" {
		b.addf("interface add bridge %s vrf %s", bridge, vrf)
		return
	}
	b.addf("interface add bridge %s", bridge)
}

// AddL2VNI creates an EVPN L2VNI VXLAN interface bound to an L2 bridge domain:
// `interface add vxlan l2vni<vni> vni <vni> local <vtep> domain <bridge>`.
func (b *Batch) AddL2VNI(vni uint32, vtep, bridge string) {
	b.addf("interface add vxlan l2vni%d vni %d local %s domain %s", vni, vni, vtep, bridge)
}

// AddTrunkVlanToBridge maps a VLAN carried on the shared fabric trunk port into
// an L2 bridge domain, so VLAN-tagged frames arriving on the trunk are bridged
// into the L2VNI. It renders a grout VLAN sub-interface enslaved to the bridge:
// `interface add vlan <trunk>.<vlan> parent <trunk> vlan_id <vlan> domain <bridge>`.
//
// This is the datapath for workloads attached with macvlan on the host-side
// trunk netdev (they tag with <vlan>): grout demuxes the trunk VLAN into the
// bridge domain. The trunk PORT itself MUST stay in grout's VRF mode (i.e. it is
// created with no `domain`), otherwise grout's iface_input skips VLAN demux and
// the sub-interface never receives frames (verified against grout's datapath).
func (b *Batch) AddTrunkVlanToBridge(trunk string, vlan uint16, bridge string) {
	b.addf("interface add vlan %s.%d parent %s vlan_id %d domain %s", trunk, vlan, trunk, vlan, bridge)
}

// AddOnLinkHostRoute installs an on-link host route to a workload reachable
// directly on iface: it allocates an L3 nexthop pointing at the workload address
// and adds the /32 or /128 route via that nexthop id in vrf. It returns the
// allocated nexthop id.
func (b *Batch) AddOnLinkHostRoute(hostCIDR, iface, vrf string) (uint32, error) {
	addr, err := hostAddr(hostCIDR)
	if err != nil {
		return 0, err
	}
	id := b.nextNHID
	b.nextNHID++
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
