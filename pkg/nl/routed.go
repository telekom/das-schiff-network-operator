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

// ReconcileRoutedPorts programs the on-link datapath for routed CNI attachments
// whose CRA-side veth was moved into this network namespace by the routed CNI.
//
// It is adopt-only: the veth itself is created and removed by the CNI, so a
// missing port is skipped (not an error) and the interface is never deleted
// here. Programming is additive/idempotent (existing addresses and routes are
// left in place); when an attachment goes away the CNI removes the veth, which
// takes its addresses and on-link routes with it.
func (n *Manager) ReconcileRoutedPorts(cfg *NetlinkConfiguration) error {
	for i := range cfg.RoutedPorts {
		if err := n.reconcileRoutedPort(&cfg.RoutedPorts[i]); err != nil {
			return fmt.Errorf("error reconciling routed port %q: %w", cfg.RoutedPorts[i].Interface, err)
		}
	}
	return nil
}

// ReconcileL2AttachedPorts enslaves the routed-CNI L2 attach ports (moved into
// this netns by the CNI) to their Layer2 bridge (l2.<vlanID>) as bridge slaves
// with no L3 addressing. Like ReconcileRoutedPorts it is adopt-only: a port that
// is not present yet (or already gone) is skipped.
func (n *Manager) ReconcileL2AttachedPorts(cfg *NetlinkConfiguration) error {
	for i := range cfg.Layer2s {
		l2 := &cfg.Layer2s[i]
		for j := range l2.AttachedPorts {
			if err := n.reconcileL2AttachedPort(l2, &l2.AttachedPorts[j]); err != nil {
				return fmt.Errorf("error reconciling L2 attached port %q (vlan %d): %w",
					l2.AttachedPorts[j].Interface, l2.VlanID, err)
			}
		}
	}
	return nil
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
	if err := n.toolkit.LinkSetMaster(link, bridgeLink); err != nil {
		return fmt.Errorf("failed to enslave port %q to bridge %q: %w", p.Interface, bridgeName, err)
	}
	if err := n.toolkit.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set L2 attached port %q up: %w", p.Interface, err)
	}
	return nil
}

func (n *Manager) reconcileRoutedPort(p *RoutedPort) error {
	// The FRR flavor programs the datapath with raw netlink and cannot back a
	// DPDK vhost-user socket; that transport is VSR-only.
	if p.Transport == transportVhostUser {
		return fmt.Errorf("routed port %q uses vhost-user transport, which is unsupported on the FRR flavor (VSR-only)", p.Interface)
	}
	link, err := n.toolkit.LinkByName(p.Interface)
	if err != nil {
		// The port is created/removed by the CNI; if it is not present (yet, or
		// already gone) there is nothing to program.
		return nil //nolint:nilerr // adopt-only: a missing port is not an error
	}

	// Determine the routing table for the on-link host routes:
	//   - tenant VRF: enslave the port to the VRF and use its table;
	//   - underlay (no VRF): keep the port in the default (main) table so the
	//     routes are advertised by the fabric/underlay BGP session.
	table := unix.RT_TABLE_MAIN
	if !isDefaultVRFName(p.VRF) {
		vrfTable, verr := n.enslaveRoutedPort(link, p.VRF)
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

// enslaveRoutedPort enslaves link to the named VRF device and returns its table.
func (n *Manager) enslaveRoutedPort(link netlink.Link, vrfName string) (int, error) {
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
