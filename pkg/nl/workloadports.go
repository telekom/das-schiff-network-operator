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
