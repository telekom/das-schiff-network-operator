//nolint:wrapcheck
package nl

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// maxVLANID is the highest assignable 802.1Q VLAN id (4095 is reserved).
const maxVLANID = 4094

// workloadPortAliasPrefix is the ifalias prefix the workload CNI stamps onto
// every CRA-side port it moves into this netns. Together with the veth link
// type it is what makes a link a workload port: a config entry merely *names*
// an interface, and the name alone must not be enough to have the datapath
// programmed onto it. It mirrors workloadcni.InfraPortPrefix (asserted by a
// test; the package itself is not imported to keep the API client out of the
// datapath layer).
const workloadPortAliasPrefix = "infra-"

// ReconcileWorkloadPorts programs the on-link datapath for workload CNI attachments
// whose CRA-side veth was moved into this network namespace by the workload CNI.
//
// It is adopt-only: the veth itself is created and removed by the CNI, so a
// missing port is skipped (not an error) and the interface is never deleted
// here. On the port itself the reconciler is authoritative: the CNI owns the
// veth exclusively, so any global address or scope-link RTPROT_BOOT route on it
// that the current attachment does not ask for is stale (a re-recorded
// attachment with a different gateway or host-route set) and is removed. When
// an attachment goes away the CNI removes the veth, which takes its addresses
// and on-link routes with it.
//
// Every entry is attempted and the failures are reported together, so one
// entry that cannot be programmed does not hold the others hostage.
func (n *Manager) ReconcileWorkloadPorts(cfg *NetlinkConfiguration) error {
	var errs []error
	for i := range cfg.WorkloadPorts {
		if err := n.reconcileWorkloadPort(&cfg.WorkloadPorts[i]); err != nil {
			errs = append(errs, fmt.Errorf("error reconciling workload port %q: %w", cfg.WorkloadPorts[i].Interface, err))
		}
	}
	return errors.Join(errs...)
}

// isWorkloadPortLink reports whether link is a CRA-side workload port created
// by the CNI: a veth carrying the CNI's ifalias for that very name. Programming
// is refused for anything else, so a NodeWorkloadPorts entry that names a
// platform link (the trunk veth, a bridge, a fabric VLAN) cannot get it
// enslaved to a tenant VRF, stripped of its addresses or exported into BGP.
func isWorkloadPortLink(link netlink.Link, name string) bool {
	return link.Type() == linkTypeVeth && link.Attrs().Alias == workloadPortAliasPrefix+name
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
// port that is not present yet (or already gone) is skipped. Its bridge
// membership and the VLAN sub-interfaces on top of it are owned here though, so
// a port whose attachment went away while the port itself lives on is released
// from its bridge, and sub-interfaces that are no longer wanted are removed.
// Like there, only a veth carrying the CNI's ifalias is programmed, so an entry
// naming a platform link cannot pull it into a tenant domain.
func (n *Manager) ReconcileL2AttachedPorts(cfg *NetlinkConfiguration) error {
	// desiredTrunks maps a trunked port to the workload-side VLAN ids it must
	// carry and desiredAccess an access port to its bridge, so members that went
	// away can be identified afterwards.
	//
	// A member that cannot be programmed is left out of the desired state and
	// reported at the end, after the cleanup passes ran, so a port whose new
	// attachment failed is released from its old domain rather than kept
	// forwarding into it. This is not a retry mechanism for a missing bridge:
	// every Layer2 carrying attached ports is part of cfg, and the caller
	// creates all of those bridges before this pass runs (and aborts the
	// sequence if it cannot), so a bridge lookup only fails on outside
	// interference. The caller's failure handling (restoring the last good
	// configuration) then puts the port back where it was.
	desiredTrunks := map[string]map[uint16]bool{}
	desiredAccess := map[string]string{}
	var errs []error
	for i := range cfg.Layer2s {
		l2 := &cfg.Layer2s[i]
		for j := range l2.AttachedPorts {
			p := &l2.AttachedPorts[j]
			if err := n.reconcileL2AttachedPort(l2, p); err != nil {
				errs = append(errs, fmt.Errorf("error reconciling L2 attached port %q (vlan %d): %w",
					p.Interface, l2.VlanID, err))
				continue
			}
			if p.VlanID == 0 {
				desiredAccess[p.Interface] = l2BridgeName(l2.VlanID)
				continue
			}
			if desiredTrunks[p.Interface] == nil {
				desiredTrunks[p.Interface] = map[uint16]bool{}
			}
			desiredTrunks[p.Interface][p.VlanID] = true
		}
	}

	links, err := n.toolkit.LinkList()
	if err != nil {
		return fmt.Errorf("failed to list links: %w", err)
	}
	byIndex := make(map[int]netlink.Link, len(links))
	for _, link := range links {
		byIndex[link.Attrs().Index] = link
	}
	// Both passes run regardless of each other's outcome: a sub-interface that
	// refuses to go must not keep a stale access member forwarding, or hide the
	// per-port errors collected above.
	if err := n.cleanupL2TrunkSubinterfaces(desiredTrunks, links, byIndex); err != nil {
		errs = append(errs, err)
	}
	if err := n.cleanupL2AccessPorts(desiredAccess, links, byIndex); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// l2BridgeName is the bridge carrying the Layer2 domain with the given VLAN id.
func l2BridgeName(vlanID int) string {
	return fmt.Sprintf("%s%d", layer2SVI, vlanID)
}

func (n *Manager) reconcileL2AttachedPort(l2 *Layer2Information, p *L2AttachedPort) error {
	link, err := n.toolkit.LinkByName(p.Interface)
	if err != nil {
		// Adopt-only: the port is created/removed by the CNI, so one that is not
		// present (yet, or any more) is simply skipped. Any other netlink failure
		// (permission, transient error, bad handle) must surface.
		var notFound netlink.LinkNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("failed to look up L2 attached port %q: %w", p.Interface, err)
	}
	if !isWorkloadPortLink(link, p.Interface) {
		return fmt.Errorf("refusing to bridge %q: not a workload CNI port (want a veth with alias %q, got %s %q)",
			p.Interface, workloadPortAliasPrefix+p.Interface, link.Type(), link.Attrs().Alias)
	}
	// Resolve the target bridge before touching the port, so that a missing
	// bridge (which the caller's Layer2 pass rules out, see
	// ReconcileL2AttachedPorts) leaves the port exactly as it was.
	bridgeName := l2BridgeName(l2.VlanID)
	bridgeLink, err := n.toolkit.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("L2 bridge %q not found for attached port %q: %w", bridgeName, p.Interface, err)
	}

	// A port that was a routed attachment before still carries that mode's
	// gateway addresses and exported host routes. They have to go before the
	// port joins a bridge: the routes would keep attracting traffic for the
	// workload's old addresses into the L2 domain, and the gateway addresses
	// would answer ARP/ND inside it.
	if err := n.reconcileWorkloadPortAddrs(link, &WorkloadPort{Interface: p.Interface}); err != nil {
		return err
	}
	if err := n.reconcileWorkloadPortRoutes(link, &WorkloadPort{Interface: p.Interface}, unix.RT_TABLE_UNSPEC); err != nil {
		return err
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

	// An attachment that moved to another domain still hangs off the old bridge;
	// leave that one explicitly before joining the new one rather than relying
	// on the kernel to swap masters on our behalf.
	if m := slave.Attrs().MasterIndex; m != 0 && m != bridgeLink.Attrs().Index {
		if err := n.toolkit.LinkSetNoMaster(slave); err != nil {
			return fmt.Errorf("failed to detach port %q from its previous bridge: %w", slave.Attrs().Name, err)
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
		if !ok || vlan.VlanId != int(vlanID) || vlan.ParentIndex != parent.Attrs().Index ||
			vlan.VlanProtocol != netlink.VLAN_PROTOCOL_8021Q {
			return nil, fmt.Errorf("interface %q exists but is not the 802.1Q vlan %d sub-interface of %q",
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
// mentions such a port) nor from bridge membership (a bridge name proves
// nothing about who created a member) but from the datapath: only this
// reconciler creates a VLAN sub-interface named <parent>.<vlanID> on a workload
// port — a veth the CNI stamped with its ifalias, the same test that gates
// programming — so exactly those links are ours to remove. That also covers
// the case where the domain went away first: its bridge is deleted before this
// runs, leaving the sub-interface without a master, and the entry is dropped
// from the desired set as a whole, which would otherwise leak one link per
// removed domain.
func (n *Manager) cleanupL2TrunkSubinterfaces(desired map[string]map[uint16]bool,
	links []netlink.Link, byIndex map[int]netlink.Link,
) error {
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
		// name it derives, on a workload port.
		if vlan.Name != l2TrunkSubinterfaceName(parent.Attrs().Name, vlanID) ||
			!isWorkloadPortLink(parent, parent.Attrs().Name) {
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

// cleanupL2AccessPorts releases workload ports that are still untagged members
// of a Layer2 bridge although no attachment wants them there (any more). The
// port itself is left alone — it is the CNI's to remove — but as long as it is
// enslaved it keeps forwarding into a domain it is no longer entitled to, which
// is exactly what dropping the attachment is meant to stop: an entry that fell
// out of the node configuration, an attachment moved to another domain that is
// not present yet, or a workload whose port outlived its sandbox teardown.
//
// Ownership is again the CNI's ifalias on a veth, not bridge membership: a
// workload port enslaved into an l2.<id> bridge is stale unless an attachment
// wants it in that very bridge, while any other member (the domain's VXLAN and
// fabric VLAN devices, or a veth this CNI did not create) is left alone. Trunk
// members are enslaved through their VLAN sub-interfaces and covered by
// cleanupL2TrunkSubinterfaces; a trunked port itself is never enslaved.
func (n *Manager) cleanupL2AccessPorts(desired map[string]string, links []netlink.Link,
	byIndex map[int]netlink.Link,
) error {
	for _, link := range links {
		if link.Attrs().MasterIndex == 0 || !isWorkloadPortLink(link, link.Attrs().Name) {
			continue
		}
		master, ok := byIndex[link.Attrs().MasterIndex]
		if !ok || master.Type() != linkTypeBridge || !strings.HasPrefix(master.Attrs().Name, layer2SVI) {
			continue
		}
		if desired[link.Attrs().Name] == master.Attrs().Name {
			continue
		}
		if err := n.toolkit.LinkSetNoMaster(link); err != nil {
			return fmt.Errorf("failed to release stale L2 attached port %q from bridge %q: %w",
				link.Attrs().Name, master.Attrs().Name, err)
		}
	}
	return nil
}

func (n *Manager) reconcileWorkloadPort(p *WorkloadPort) error {
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
	if !isWorkloadPortLink(link, p.Interface) {
		return fmt.Errorf("refusing to program %q: not a workload CNI port (want a veth with alias %q, got %s %q)",
			p.Interface, workloadPortAliasPrefix+p.Interface, link.Type(), link.Attrs().Alias)
	}

	// Determine the routing table for the on-link host routes:
	//   - tenant VRF: enslave the port to the VRF and use its table;
	//   - underlay (no VRF): keep the port in the default (main) table so the
	//     routes are advertised by the fabric/underlay BGP session. A port that
	//     was previously bound to a VRF is released, otherwise its host routes
	//     would land in the main table while the port still forwards in the VRF.
	table := unix.RT_TABLE_MAIN
	if isDefaultVRFName(p.VRF) {
		if link.Attrs().MasterIndex != 0 {
			if err := n.toolkit.LinkSetNoMaster(link); err != nil {
				return fmt.Errorf("failed to release port %q from its VRF: %w", p.Interface, err)
			}
		}
	} else {
		vrfTable, verr := n.enslaveWorkloadPort(link, p.VRF)
		if verr != nil {
			return verr
		}
		table = vrfTable
	}

	if err := n.toolkit.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set port %q up: %w", p.Interface, err)
	}

	if err := n.reconcileWorkloadPortAddrs(link, p); err != nil {
		return err
	}
	return n.reconcileWorkloadPortRoutes(link, p, table)
}

// reconcileWorkloadPortAddrs makes the port carry exactly the attachment's
// on-link gateway addresses. The kernel's own (non-host-prefix) link-local
// address is left alone.
func (n *Manager) reconcileWorkloadPortAddrs(link netlink.Link, p *WorkloadPort) error {
	gateways := []string{p.GatewayV4, p.GatewayV6}
	desired := make([]*netlink.Addr, 0, len(gateways))
	for _, gw := range gateways {
		if gw == "" {
			continue
		}
		addr, err := n.toolkit.ParseAddr(gw)
		if err != nil {
			return fmt.Errorf("invalid gateway address %q: %w", gw, err)
		}
		desired = append(desired, addr)
	}

	current, err := n.toolkit.AddrList(link, unix.AF_UNSPEC)
	if err != nil {
		return fmt.Errorf("failed to list addresses of port %q: %w", p.Interface, err)
	}
	// The gateways are single-address prefixes (/32, /128; ValidateEntry
	// enforces it) and so is everything this reconciler ever adds; anything
	// wider — the kernel's own fe80::/64 above all — is not ours. Link-locality
	// cannot tell them apart, since the default gateways are link-local too.
	for i := range current {
		if ones, bits := current[i].Mask.Size(); ones != bits || containsNetlinkAddress(desired, &current[i]) {
			continue
		}
		if err := n.toolkit.AddrDel(link, &current[i]); err != nil {
			return fmt.Errorf("failed to remove stale address %s from port %q: %w", current[i].IPNet, p.Interface, err)
		}
	}
	for _, addr := range desired {
		if err := n.toolkit.AddrAdd(link, addr); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("failed to add gateway address %s: %w", addr.IPNet, err)
		}
	}
	return nil
}

// reconcileWorkloadPortRoutes makes the port carry exactly the attachment's
// scope-link host routes in table. Only RTPROT_BOOT routes are considered ours:
// the kernel's own routes for the port (local/multicast, RTPROT_KERNEL) and
// anything a routing daemon may install are left alone.
func (n *Manager) reconcileWorkloadPortRoutes(link netlink.Link, p *WorkloadPort, table int) error {
	desired := make([]*netlink.Route, 0, len(p.HostRoutes))
	for _, hr := range p.HostRoutes {
		_, dst, err := net.ParseCIDR(hr)
		if err != nil {
			return fmt.Errorf("invalid host route %q: %w", hr, err)
		}
		desired = append(desired, &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       dst,
			Scope:     netlink.SCOPE_LINK,
			Table:     table,
			Protocol:  unix.RTPROT_BOOT,
		})
	}

	// Table 0 with RT_FILTER_TABLE lists every table: a route left behind in a
	// previous VRF's table is stale as well.
	current, err := n.toolkit.RouteListFiltered(netlink.FAMILY_ALL,
		&netlink.Route{LinkIndex: link.Attrs().Index, Table: unix.RT_TABLE_UNSPEC},
		netlink.RT_FILTER_OIF|netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("failed to list routes of port %q: %w", p.Interface, err)
	}
	// Only the on-link (gateway-less) routes this reconciler installs are
	// candidates. The scope is not usable to tell them apart: IPv6 routes are
	// always reported with universe scope, whatever they were added with.
	for i := range current {
		r := &current[i]
		if r.Protocol != unix.RTPROT_BOOT || r.Gw != nil || r.Dst == nil ||
			containsWorkloadHostRoute(desired, r) {
			continue
		}
		if err := n.toolkit.RouteDel(r); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("failed to remove stale host route %s from port %q: %w", r.Dst, p.Interface, err)
		}
	}
	for _, route := range desired {
		err := n.toolkit.RouteAdd(route)
		if errors.Is(err, unix.EEXIST) {
			// The listing above was scoped to this port, so EEXIST may as well
			// mean the prefix is already routed elsewhere in this table.
			err = n.checkHostRouteOwner(route, p)
		}
		if err != nil {
			return fmt.Errorf("failed to add on-link host route %s: %w", route.Dst, err)
		}
	}
	return nil
}

// checkHostRouteOwner resolves an EEXIST from adding route: the same host route
// via this very port is fine (already there), but one that points at another
// interface means two ports claim the same address in one table. That is
// refused rather than taken over: the address would otherwise flap between
// ports on every reconcile, and the stale one keeps attracting traffic, so the
// conflict has to be resolved by the attachment that owns it going away.
func (n *Manager) checkHostRouteOwner(route *netlink.Route, p *WorkloadPort) error {
	existing, err := n.toolkit.RouteListFiltered(netlink.FAMILY_ALL,
		&netlink.Route{Dst: route.Dst, Table: route.Table},
		netlink.RT_FILTER_DST|netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("failed to look up existing route in table %d: %w", route.Table, err)
	}
	for i := range existing {
		if existing[i].LinkIndex != route.LinkIndex {
			return fmt.Errorf("already routed via another interface (index %d) in table %d; refusing to take it over for port %q",
				existing[i].LinkIndex, route.Table, p.Interface)
		}
	}
	return nil
}

// containsWorkloadHostRoute reports whether list holds a route for r's
// destination in r's table.
func containsWorkloadHostRoute(list []*netlink.Route, r *netlink.Route) bool {
	for _, d := range list {
		if d.Table == r.Table && d.Dst.IP.Equal(r.Dst.IP) && slices.Equal(d.Dst.Mask, r.Dst.Mask) {
			return true
		}
	}
	return false
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

// isDefaultVRFName reports whether name denotes the underlay/default table.
func isDefaultVRFName(name string) bool {
	switch strings.ToLower(name) {
	case "", "default", "main":
		return true
	default:
		return false
	}
}
