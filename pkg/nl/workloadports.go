//nolint:wrapcheck
package nl

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// transportVhostUser is the vhost-user transport value. It is VSR-only: the FRR
// flavor programs the datapath with raw netlink and cannot back a DPDK vhost
// socket, so a port declaring it is rejected.
const transportVhostUser = "vhostuser"

// maxVLANID is the highest assignable 802.1Q VLAN id (4095 is reserved).
const maxVLANID = 4094

// ReconcileWorkloadPorts programs the on-link datapath for workload CNI attachments
// whose CRA-side veth was moved into this network namespace by the workload CNI.
//
// It is adopt-only: the veth itself is created and removed by the CNI, so a
// missing port is skipped (not an error) and the interface is never deleted
// here. Programming is additive/idempotent (existing addresses and routes are
// left in place); when an attachment goes away the CNI removes the veth, which
// takes its addresses and on-link routes with it.
func (n *Manager) ReconcileWorkloadPorts(cfg *NetlinkConfiguration) error {
	for i := range cfg.WorkloadPorts {
		if err := n.reconcileWorkloadPort(&cfg.WorkloadPorts[i]); err != nil {
			return fmt.Errorf("error reconciling workload port %q: %w", cfg.WorkloadPorts[i].Interface, err)
		}
	}
	return nil
}

// ReconcileL2AttachedPorts attaches the workload-CNI L2 attach ports (moved into
// this netns by the CNI) to their Layer2 bridge (l2.<vlanID>) with no L3
// addressing. An access port (VlanID 0) is enslaved directly, so it is an
// untagged member of exactly one domain. A trunk member (VlanID set) is instead
// reached through an <interface>.<vlanID> VLAN sub-interface which is enslaved
// in its place, so several domains can share one port and the workload-side id
// may differ from the domain's fabric-side VLAN id. A trunked port itself is
// never enslaved, so untagged frames and frames carrying an unlisted VLAN id
// are not forwarded anywhere.
//
// Like ReconcileWorkloadPorts it is adopt-only with respect to the moved port: a
// port that is not present yet (or already gone) is skipped. The VLAN
// sub-interfaces on top of it are owned here, so ones that are no longer wanted
// are removed.
func (n *Manager) ReconcileL2AttachedPorts(cfg *NetlinkConfiguration) error {
	// desired maps a trunked port to the workload-side VLAN ids it must carry, so
	// sub-interfaces of members that went away can be identified afterwards.
	desired := map[string]map[uint16]bool{}
	for i := range cfg.Layer2s {
		l2 := &cfg.Layer2s[i]
		for j := range l2.AttachedPorts {
			p := &l2.AttachedPorts[j]
			if err := n.reconcileL2AttachedPort(l2, p); err != nil {
				return fmt.Errorf("error reconciling L2 attached port %q (vlan %d): %w",
					p.Interface, l2.VlanID, err)
			}
			if p.VlanID == 0 {
				continue
			}
			if desired[p.Interface] == nil {
				desired[p.Interface] = map[uint16]bool{}
			}
			desired[p.Interface][p.VlanID] = true
		}
	}
	return n.cleanupL2TrunkSubinterfaces(desired)
}

func (n *Manager) reconcileL2AttachedPort(l2 *Layer2Information, p *L2AttachedPort) error {
	if p.Transport == transportVhostUser {
		return fmt.Errorf("L2 attached port %q uses vhost-user transport, which is unsupported on the FRR flavor (VSR-only)", p.Interface)
	}

	link, err := n.toolkit.LinkByName(p.Interface)
	if err != nil {
		// Adopt-only: the port is created/removed by the CNI.
		return nil //nolint:nilerr // a missing port is not an error
	}

	bridgeName := fmt.Sprintf("%s%d", layer2SVI, l2.VlanID)
	bridgeLink, err := n.toolkit.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("L2 bridge %q not found for attached port %q: %w", bridgeName, p.Interface, err)
	}

	// A trunk member is enslaved through its VLAN sub-interface; the port itself
	// stays unenslaved so it carries nothing but the tags that have a member.
	slave := link
	if p.VlanID != 0 {
		// A port that was an untagged access member before must not stay one, or
		// the trunk would keep leaking its untagged and unmapped-tag traffic into
		// that domain.
		if link.Attrs().MasterIndex != 0 {
			if err := n.toolkit.LinkSetNoMaster(link); err != nil {
				return fmt.Errorf("failed to detach trunked port %q from its bridge: %w", p.Interface, err)
			}
		}
		// The parent must be up for the sub-interface to pass traffic.
		if err := n.toolkit.LinkSetUp(link); err != nil {
			return fmt.Errorf("failed to set L2 attached port %q up: %w", p.Interface, err)
		}
		if slave, err = n.ensureL2TrunkSubinterface(link, p.VlanID); err != nil {
			return err
		}
	}

	if err := n.toolkit.LinkSetMaster(slave, bridgeLink); err != nil {
		return fmt.Errorf("failed to enslave port %q to bridge %q: %w", slave.Attrs().Name, bridgeName, err)
	}
	if err := n.toolkit.LinkSetUp(slave); err != nil {
		return fmt.Errorf("failed to set L2 attached port %q up: %w", slave.Attrs().Name, err)
	}
	return nil
}

// l2TrunkSubinterfaceName is the netdev carrying one tagged member of a trunk
// port. It matches the interface name the VSR flavor renders for the same
// member, so both flavors expose the same datapath naming.
func l2TrunkSubinterfaceName(parent string, vlanID uint16) string {
	return fmt.Sprintf("%s.%d", parent, vlanID)
}

// ensureL2TrunkSubinterface creates (or adopts) the VLAN sub-interface carrying
// vlanID on parent and returns it. An existing interface of that name is only
// adopted if it really is a VLAN interface for that id on that parent;
// otherwise reconciliation would silently bridge an unrelated netdev into a
// tenant domain.
//
// The sub-interface inherits the port's MTU, which the CNI set from the
// attachment's own mtu: the kernel refuses a child MTU above the parent's, so
// the port is the single source of truth for it. The 4 bytes the tag adds on
// the wire are therefore the workload's to account for.
func (n *Manager) ensureL2TrunkSubinterface(parent netlink.Link, vlanID uint16) (netlink.Link, error) {
	name := l2TrunkSubinterfaceName(parent.Attrs().Name, vlanID)
	if len(name) >= unix.IFNAMSIZ {
		return nil, fmt.Errorf("trunk sub-interface name %q exceeds %d characters", name, unix.IFNAMSIZ-1)
	}

	existing, err := n.toolkit.LinkByName(name)
	if err == nil {
		vlan, ok := existing.(*netlink.Vlan)
		if !ok || vlan.VlanId != int(vlanID) || vlan.ParentIndex != parent.Attrs().Index {
			return nil, fmt.Errorf("interface %q exists but is not the vlan %d sub-interface of %q",
				name, vlanID, parent.Attrs().Name)
		}
		return existing, nil
	}
	var notFound netlink.LinkNotFoundError
	if !errors.As(err, &notFound) {
		return nil, fmt.Errorf("failed to look up trunk sub-interface %q: %w", name, err)
	}

	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	attrs.ParentIndex = parent.Attrs().Index
	vlan := &netlink.Vlan{
		LinkAttrs:    attrs,
		VlanId:       int(vlanID),
		VlanProtocol: netlink.VLAN_PROTOCOL_8021Q,
	}
	if err := n.toolkit.LinkAdd(vlan); err != nil {
		return nil, fmt.Errorf("failed to create trunk sub-interface %q: %w", name, err)
	}
	link, err := n.toolkit.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("failed to look up created trunk sub-interface %q: %w", name, err)
	}
	return link, nil
}

// cleanupL2TrunkSubinterfaces removes the VLAN sub-interfaces that are no longer
// members of any Layer2 domain. Dropping a member while the workload stays up
// would otherwise leave the old sub-interface bridged into the domain it was
// just detached from, and that has to hold even when the port loses its last
// member: a trunk turning into an access port, or an attachment dropped as a
// whole while its port lives on, must not keep forwarding.
//
// Ownership is therefore not derived from the desired set (which no longer
// mentions such a port) but from the datapath: only this reconciler enslaves a
// VLAN sub-interface named <parent>.<vlanID> into an l2.<id> bridge, so exactly
// those links are ours to remove.
func (n *Manager) cleanupL2TrunkSubinterfaces(desired map[string]map[uint16]bool) error {
	links, err := n.toolkit.LinkList()
	if err != nil {
		return fmt.Errorf("failed to list links: %w", err)
	}
	byIndex := map[int]netlink.Link{}
	for _, link := range links {
		byIndex[link.Attrs().Index] = link
	}

	for _, link := range links {
		vlan, ok := link.(*netlink.Vlan)
		if !ok || vlan.VlanId < 0 || vlan.VlanId > maxVLANID {
			continue
		}
		parent, ok := byIndex[vlan.ParentIndex]
		if !ok {
			continue
		}
		vlanID := uint16(vlan.VlanId)
		// Only ever remove interfaces this reconciler could have created: the
		// name it derives, on a link it enslaved into a Layer2 bridge.
		if vlan.Name != l2TrunkSubinterfaceName(parent.Attrs().Name, vlanID) {
			continue
		}
		master, hasMaster := byIndex[vlan.MasterIndex]
		enslavedIntoL2 := hasMaster && strings.HasPrefix(master.Attrs().Name, layer2SVI)
		_, trunkedParent := desired[parent.Attrs().Name]
		// Enslaved into a Layer2 domain, or hanging off a port this reconciler
		// trunks: either way it is one of ours. A sub-interface that lost its
		// master would otherwise be left behind forever.
		if !enslavedIntoL2 && !trunkedParent {
			continue
		}
		if desired[parent.Attrs().Name][vlanID] {
			continue
		}
		if err := n.toolkit.LinkDel(link); err != nil {
			return fmt.Errorf("failed to delete stale trunk sub-interface %q: %w", vlan.Name, err)
		}
	}
	return nil
}

func (n *Manager) reconcileWorkloadPort(p *WorkloadPort) error {
	// The FRR flavor programs the datapath with raw netlink and cannot back a
	// DPDK vhost-user socket; that transport is VSR-only.
	if p.Transport == transportVhostUser {
		return fmt.Errorf("workload port %q uses vhost-user transport, which is unsupported on the FRR flavor (VSR-only)", p.Interface)
	}
	link, err := n.toolkit.LinkByName(p.Interface)
	if err != nil {
		// The port is created/removed by the CNI; if it is not present (yet, or
		// already gone) there is nothing to program. Any other netlink failure
		// (permission, transient error, bad handle) must surface.
		var notFound netlink.LinkNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("failed to look up workload port %q: %w", p.Interface, err)
	}

	// Determine the routing table for the on-link host routes:
	//   - tenant VRF: enslave the port to the VRF and use its table;
	//   - underlay (no VRF): keep the port in the default (main) table so the
	//     routes are advertised by the fabric/underlay BGP session.
	table := unix.RT_TABLE_MAIN
	if !isDefaultVRFName(p.VRF) {
		vrfTable, verr := n.enslaveWorkloadPort(link, p.VRF)
		if verr != nil {
			return verr
		}
		table = vrfTable
	}

	if err := n.toolkit.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set port %q up: %w", p.Interface, err)
	}

	for _, gw := range []string{p.GatewayV4, p.GatewayV6} {
		if gw == "" {
			continue
		}
		if err := n.addRoutedAddr(link, gw); err != nil {
			return err
		}
	}

	for _, hr := range p.HostRoutes {
		if err := n.addRoutedHostRoute(link, hr, table); err != nil {
			return err
		}
	}
	return nil
}

// enslaveWorkloadPort enslaves link to the named VRF device and returns its table.
func (n *Manager) enslaveWorkloadPort(link netlink.Link, vrfName string) (int, error) {
	vrfLink, err := n.toolkit.LinkByName(vrfName)
	if err != nil {
		return 0, fmt.Errorf("failed to find VRF %q: %w", vrfName, err)
	}
	vrf, ok := vrfLink.(*netlink.Vrf)
	if !ok {
		return 0, fmt.Errorf("interface %q is not a VRF", vrfName)
	}
	if err := n.toolkit.LinkSetMaster(link, vrf); err != nil {
		return 0, fmt.Errorf("failed to enslave port to VRF %q: %w", vrfName, err)
	}
	return int(vrf.Table), nil
}

// addRoutedAddr adds an on-link gateway address (CIDR) to the port, ignoring an
// "already exists" error so reconciliation is idempotent.
func (n *Manager) addRoutedAddr(link netlink.Link, cidr string) error {
	addr, err := n.toolkit.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("invalid gateway address %q: %w", cidr, err)
	}
	if err := n.toolkit.AddrAdd(link, addr); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("failed to add gateway address %q: %w", cidr, err)
	}
	return nil
}

// addRoutedHostRoute installs a scope-link host route (/32 or /128) for the
// workload address via the port in the given table. RTPROT_BOOT marks it as a
// plain kernel/boot route so FRR classifies it as ZEBRA_ROUTE_KERNEL and picks
// it up via `redistribute kernel`.
func (n *Manager) addRoutedHostRoute(link netlink.Link, cidr string, table int) error {
	_, dst, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid host route %q: %w", cidr, err)
	}
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst,
		Scope:     netlink.SCOPE_LINK,
		Table:     table,
		Protocol:  unix.RTPROT_BOOT,
	}
	if err := n.toolkit.RouteAdd(route); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("failed to add on-link host route %q: %w", cidr, err)
	}
	return nil
}

// isDefaultVRFName reports whether name denotes the underlay/default table.
func isDefaultVRFName(name string) bool {
	switch strings.ToLower(name) {
	case "", "default", "main":
		return true
	default:
		return false
	}
}
