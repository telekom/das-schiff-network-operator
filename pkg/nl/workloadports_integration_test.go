//go:build linux

/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nl

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/containernetworking/plugins/pkg/testutils"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// These tests validate the CRA-side FIB programming that used to live in the
// CNI (pkg/cni) and now lives in the agent-driven frr-cra datapath. They run in
// a private netns and require root (CAP_NET_ADMIN).

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("integration test requires root (netns + netlink)")
	}
}

const routedTestVRFTable = 1234

// addVethPort creates a veth pair whose one end stands in for the CNI-moved
// workload port; unlike a dummy it has the link type the reconciler requires.
func addVethPort(name string) error {
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		PeerName:  name + "p",
	}); err != nil {
		return fmt.Errorf("adding veth port: %w", err)
	}
	return nil
}

// addEnslavedVethPort is addVethPort plus enslaving the port into master, the
// way something other than this reconciler would have put a veth into a bridge.
func addEnslavedVethPort(name string, master netlink.Link) error {
	if err := addVethPort(name); err != nil {
		return err
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("looking up veth port: %w", err)
	}
	if err := netlink.LinkSetMaster(link, master); err != nil {
		return fmt.Errorf("enslaving veth port: %w", err)
	}
	return nil
}

// addWorkloadVethPort is addVethPort plus the ifalias the CNI stamps onto every
// CRA-side port, which is what makes the reconciler accept the link as a
// workload port.
func addWorkloadVethPort(name string) error {
	if err := addVethPort(name); err != nil {
		return err
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("looking up veth port: %w", err)
	}
	if err := netlink.LinkSetAlias(link, workloadPortAliasPrefix+name); err != nil {
		return fmt.Errorf("setting veth port alias: %w", err)
	}
	return nil
}

func TestReconcileWorkloadPortsVRF(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const port = "cra0123456789a"
	if derr := testNS.Do(func(_ ns.NetNS) error {
		vrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "cluster"}, Table: routedTestVRFTable}
		if e := netlink.LinkAdd(vrf); e != nil {
			return fmt.Errorf("adding vrf: %w", e)
		}
		if e := netlink.LinkSetUp(vrf); e != nil {
			return fmt.Errorf("setting vrf up: %w", e)
		}
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		WorkloadPorts: []WorkloadPort{{
			Interface: port,
			VRF:       "cluster",
			GatewayV4: "169.254.1.1/32",
			GatewayV6: "fe80::1/128",
			HostRoutes: []string{
				"10.100.0.5/32",
				"fd00:100::5/128",
			},
		}},
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileWorkloadPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileWorkloadPorts: %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		link, e := netlink.LinkByName(port)
		if e != nil {
			t.Errorf("port %s missing: %v", port, e)
			return nil
		}
		vrfLink, _ := netlink.LinkByName("cluster")
		if link.Attrs().MasterIndex != vrfLink.Attrs().Index {
			t.Errorf("port not enslaved to cluster VRF (master=%d, want %d)",
				link.Attrs().MasterIndex, vrfLink.Attrs().Index)
		}
		assertHostRoutes(t, routedTestVRFTable, "10.100.0.5/32", "fd00:100::5/128")
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

func TestReconcileWorkloadPortsUnderlayMainTable(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const port = "craunderlay01"
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	// No VRF => underlay: on-link host routes land in the default (main) table.
	cfg := &NetlinkConfiguration{
		WorkloadPorts: []WorkloadPort{{
			Interface: port,
			GatewayV4: "169.254.1.1/32",
			GatewayV6: "fe80::1/128",
			HostRoutes: []string{
				"10.200.0.7/32",
				"fd00:200::7/128",
			},
		}},
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileWorkloadPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileWorkloadPorts: %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		link, e := netlink.LinkByName(port)
		if e != nil {
			t.Errorf("port %s missing: %v", port, e)
			return nil
		}
		// Underlay: the port must NOT be enslaved to any master.
		if link.Attrs().MasterIndex != 0 {
			t.Errorf("underlay port unexpectedly enslaved (master=%d)", link.Attrs().MasterIndex)
		}
		assertHostRoutes(t, unix.RT_TABLE_MAIN, "10.200.0.7/32", "fd00:200::7/128")
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileWorkloadPortsReplacesStale ensures a re-recorded attachment with
// a different gateway and host-route set does not leave the previous values on
// the port: the CNI owns the veth, so anything else on it is stale.
func TestReconcileWorkloadPortsReplacesStale(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const port = "crareplace01"
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	first := &NetlinkConfiguration{WorkloadPorts: []WorkloadPort{{
		Interface:  port,
		GatewayV4:  "169.254.1.1/32",
		GatewayV6:  "fe80::1/128",
		HostRoutes: []string{"10.200.0.7/32", "fd00:200::7/128"},
	}}}
	second := &NetlinkConfiguration{WorkloadPorts: []WorkloadPort{{
		Interface:  port,
		GatewayV4:  "169.254.2.1/32",
		HostRoutes: []string{"10.200.0.8/32", "fd00:200::8/128"},
	}}}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		if e := mgr.ReconcileWorkloadPorts(first); e != nil {
			return e
		}
		return mgr.ReconcileWorkloadPorts(second)
	}); derr != nil {
		t.Fatalf("ReconcileWorkloadPorts: %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		link, e := netlink.LinkByName(port)
		if e != nil {
			return fmt.Errorf("port %s missing: %w", port, e)
		}
		addrs, e := netlink.AddrList(link, unix.AF_UNSPEC)
		if e != nil {
			return fmt.Errorf("listing addresses of %s: %w", port, e)
		}
		// Only the new gateway may remain besides the kernel's own fe80::/64:
		// the old gateways are link-local as well and must have been removed.
		var got []string
		for _, a := range addrs {
			if ones, bits := a.Mask.Size(); ones != bits {
				continue
			}
			got = append(got, a.IPNet.String())
		}
		if len(got) != 1 || got[0] != "169.254.2.1/32" {
			t.Errorf("unexpected addresses %v on port, want only [169.254.2.1/32]", got)
		}
		assertHostRoutes(t, unix.RT_TABLE_MAIN, "10.200.0.8/32", "fd00:200::8/128")
		routes, _ := netlink.RouteListFiltered(netlink.FAMILY_ALL,
			&netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
		for i := range routes {
			if routes[i].Dst == nil {
				continue
			}
			switch routes[i].Dst.String() {
			case "10.200.0.7/32", "fd00:200::7/128":
				t.Errorf("stale host route %s still present", routes[i].Dst)
			}
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileWorkloadPortsReleasesVRF ensures a port that was bound to a VRF
// is released from it when the attachment now targets the underlay, so its host
// routes and its forwarding end up in the same (main) table.
func TestReconcileWorkloadPortsReleasesVRF(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const port = "crarelease0001"
	if derr := testNS.Do(func(_ ns.NetNS) error {
		vrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "tenant"}, Table: routedTestVRFTable}
		if e := netlink.LinkAdd(vrf); e != nil {
			return fmt.Errorf("adding vrf: %w", e)
		}
		if e := netlink.LinkSetUp(vrf); e != nil {
			return fmt.Errorf("setting vrf up: %w", e)
		}
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	routes := []string{"10.100.0.9/32", "fd00:100::9/128"}
	inVRF := &NetlinkConfiguration{WorkloadPorts: []WorkloadPort{{
		Interface: port, VRF: "tenant", GatewayV4: "169.254.1.1/32", GatewayV6: "fe80::1/128", HostRoutes: routes,
	}}}
	underlay := &NetlinkConfiguration{WorkloadPorts: []WorkloadPort{{
		Interface: port, GatewayV4: "169.254.1.1/32", GatewayV6: "fe80::1/128", HostRoutes: routes,
	}}}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		if e := mgr.ReconcileWorkloadPorts(inVRF); e != nil {
			return e
		}
		link, e := netlink.LinkByName(port)
		if e != nil {
			return fmt.Errorf("looking up port: %w", e)
		}
		if link.Attrs().MasterIndex == 0 {
			t.Fatalf("precondition failed: port not enslaved to the VRF")
		}
		return mgr.ReconcileWorkloadPorts(underlay)
	}); derr != nil {
		t.Fatalf("ReconcileWorkloadPorts: %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		link, e := netlink.LinkByName(port)
		if e != nil {
			return fmt.Errorf("looking up port: %w", e)
		}
		if link.Attrs().MasterIndex != 0 {
			t.Errorf("port still enslaved after moving to the underlay (master=%d)", link.Attrs().MasterIndex)
		}
		assertHostRoutes(t, unix.RT_TABLE_MAIN, "10.100.0.9/32", "fd00:100::9/128")
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileWorkloadPortsMovesBetweenVRFs ensures a port re-targeted from one
// tenant VRF straight to another is re-enslaved (the kernel detaches the old
// master on IFLA_MASTER) and its host routes follow into the new VRF's table
// and vanish from the old one.
func TestReconcileWorkloadPortsMovesBetweenVRFs(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const (
		port       = "cramove0000001"
		otherTable = routedTestVRFTable + 1
	)
	if derr := testNS.Do(func(_ ns.NetNS) error {
		for name, table := range map[string]uint32{"tenant-a": routedTestVRFTable, "tenant-b": otherTable} {
			vrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: name}, Table: table}
			if e := netlink.LinkAdd(vrf); e != nil {
				return fmt.Errorf("adding vrf %s: %w", name, e)
			}
			if e := netlink.LinkSetUp(vrf); e != nil {
				return fmt.Errorf("setting vrf %s up: %w", name, e)
			}
		}
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	routes := []string{"10.100.0.11/32", "fd00:100::11/128"}
	inA := &NetlinkConfiguration{WorkloadPorts: []WorkloadPort{{
		Interface: port, VRF: "tenant-a", GatewayV4: "169.254.1.1/32", GatewayV6: "fe80::1/128", HostRoutes: routes,
	}}}
	inB := &NetlinkConfiguration{WorkloadPorts: []WorkloadPort{{
		Interface: port, VRF: "tenant-b", GatewayV4: "169.254.1.1/32", GatewayV6: "fe80::1/128", HostRoutes: routes,
	}}}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		if e := mgr.ReconcileWorkloadPorts(inA); e != nil {
			return fmt.Errorf("first reconcile: %w", e)
		}
		if e := mgr.ReconcileWorkloadPorts(inB); e != nil {
			return fmt.Errorf("re-targeting reconcile: %w", e)
		}
		link, e := netlink.LinkByName(port)
		if e != nil {
			return fmt.Errorf("looking up port: %w", e)
		}
		vrfB, e := netlink.LinkByName("tenant-b")
		if e != nil {
			return fmt.Errorf("looking up tenant-b: %w", e)
		}
		if link.Attrs().MasterIndex != vrfB.Attrs().Index {
			t.Errorf("port not enslaved to tenant-b (master=%d, want %d)", link.Attrs().MasterIndex, vrfB.Attrs().Index)
		}
		assertHostRoutes(t, otherTable, routes[0], routes[1])
		stale, e := netlink.RouteListFiltered(netlink.FAMILY_ALL,
			&netlink.Route{Table: routedTestVRFTable}, netlink.RT_FILTER_TABLE)
		if e != nil {
			return fmt.Errorf("listing tenant-a routes: %w", e)
		}
		for i := range stale {
			if stale[i].Dst != nil && stale[i].LinkIndex == link.Attrs().Index {
				t.Errorf("stale host route %s still in the old VRF table", stale[i].Dst)
			}
		}
		return nil
	}); derr != nil {
		t.Fatalf("ReconcileWorkloadPorts: %v", derr)
	}
}

// TestReconcileWorkloadPortsAdoptOnly ensures a missing port is skipped without an
// error (the CNI owns the veth lifecycle) and that reconciliation is idempotent.
func TestReconcileWorkloadPortsAdoptOnly(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		WorkloadPorts: []WorkloadPort{{
			Interface:  "cramissing000",
			HostRoutes: []string{"10.10.0.1/32"},
		}},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		// Missing port: must be a no-op, not an error.
		if e := mgr.ReconcileWorkloadPorts(cfg); e != nil {
			return e
		}
		// Add the port and reconcile twice to confirm idempotency.
		if e := addWorkloadVethPort("craidem000001"); e != nil {
			return e
		}
		cfg.WorkloadPorts[0].Interface = "craidem000001"
		cfg.WorkloadPorts[0].GatewayV4 = "169.254.9.9/32"
		if e := mgr.ReconcileWorkloadPorts(cfg); e != nil {
			return e
		}
		return mgr.ReconcileWorkloadPorts(cfg)
	}); derr != nil {
		t.Fatalf("adopt-only reconcile: %v", derr)
	}
}

// TestReconcileWorkloadPortsRefusesRouteTakeover covers two ports claiming the
// same workload address in one table: the kernel answers the second add with
// EEXIST, which must not be mistaken for "already installed". The route stays
// with the first port, the conflict is reported for the second one and the
// rest of that port's programming still happens.
func TestReconcileWorkloadPortsRefusesRouteTakeover(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const (
		first  = "crafirst00001"
		second = "crasecond0001"
	)
	if derr := testNS.Do(func(_ ns.NetNS) error {
		vrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "tenant-a"}, Table: routedTestVRFTable}
		if e := netlink.LinkAdd(vrf); e != nil {
			return fmt.Errorf("adding vrf: %w", e)
		}
		if e := addWorkloadVethPort(first); e != nil {
			return e
		}
		return addWorkloadVethPort(second)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	port := func(name string) WorkloadPort {
		return WorkloadPort{
			Interface: name, VRF: "tenant-a", GatewayV4: "169.254.1.1/32", GatewayV6: "fe80::1/128",
			HostRoutes: []string{"10.100.0.21/32", "fd00:100::21/128"},
		}
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		if e := mgr.ReconcileWorkloadPorts(&NetlinkConfiguration{WorkloadPorts: []WorkloadPort{port(first)}}); e != nil {
			return fmt.Errorf("programming the first port: %w", e)
		}
		e := mgr.ReconcileWorkloadPorts(&NetlinkConfiguration{WorkloadPorts: []WorkloadPort{port(first), port(second)}})
		if e == nil {
			t.Fatal("expected the second port's claim on the same address to be refused")
		}
		if !strings.Contains(e.Error(), fmt.Sprintf("workload port %q", second)) ||
			!strings.Contains(e.Error(), "another interface") {
			t.Errorf("unexpected error: %v", e)
		}
		// Re-running with only the first port must still be clean: nothing of
		// the refused claim may have displaced it.
		if e := mgr.ReconcileWorkloadPorts(&NetlinkConfiguration{WorkloadPorts: []WorkloadPort{port(first)}}); e != nil {
			return fmt.Errorf("re-programming the first port: %w", e)
		}
		firstLink, e := netlink.LinkByName(first)
		if e != nil {
			return fmt.Errorf("looking up %s: %w", first, e)
		}
		routes, e := netlink.RouteListFiltered(netlink.FAMILY_ALL,
			&netlink.Route{Table: routedTestVRFTable}, netlink.RT_FILTER_TABLE)
		if e != nil {
			return fmt.Errorf("listing routes: %w", e)
		}
		for i := range routes {
			if routes[i].Dst == nil || routes[i].Protocol != unix.RTPROT_BOOT {
				continue
			}
			if routes[i].LinkIndex != firstLink.Attrs().Index {
				t.Errorf("host route %s was taken over by link index %d, want %s (%d)",
					routes[i].Dst, routes[i].LinkIndex, first, firstLink.Attrs().Index)
			}
		}
		assertHostRoutes(t, routedTestVRFTable, "10.100.0.21/32", "fd00:100::21/128")
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileWorkloadPortsRefusesForeignLinks ensures an entry that merely
// names an interface cannot get the datapath programmed onto it: a link that is
// not a veth, or a veth without the CNI's alias, is refused and left untouched
// (no VRF master, no gateway address), while the other entries of the same
// configuration are still programmed.
func TestReconcileWorkloadPortsRefusesForeignLinks(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const (
		platform = "hbn"           // a dummy standing in for the trunk
		unowned  = "crastranger01" // a veth without the CNI alias
		owned    = "craowned00001"
	)
	if derr := testNS.Do(func(_ ns.NetNS) error {
		vrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "tenant-a"}, Table: routedTestVRFTable}
		if e := netlink.LinkAdd(vrf); e != nil {
			return fmt.Errorf("adding vrf: %w", e)
		}
		if e := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: platform}}); e != nil {
			return fmt.Errorf("adding platform link: %w", e)
		}
		if e := addVethPort(unowned); e != nil {
			return e
		}
		return addWorkloadVethPort(owned)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	port := func(name string) WorkloadPort {
		return WorkloadPort{Interface: name, VRF: "tenant-a", GatewayV4: "169.254.1.1/32", HostRoutes: []string{"10.100.0.13/32"}}
	}
	cfg := &NetlinkConfiguration{WorkloadPorts: []WorkloadPort{port(platform), port(unowned), port(owned)}}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		err := mgr.ReconcileWorkloadPorts(cfg)
		if err == nil {
			t.Fatal("expected the foreign links to be refused")
		}
		for _, name := range []string{platform, unowned} {
			if !strings.Contains(err.Error(), fmt.Sprintf("workload port %q", name)) {
				t.Errorf("error does not name refused link %q: %v", name, err)
			}
			link, e := netlink.LinkByName(name)
			if e != nil {
				return fmt.Errorf("looking up %s: %w", name, e)
			}
			if link.Attrs().MasterIndex != 0 {
				t.Errorf("%s was enslaved (master=%d) despite not being a workload port", name, link.Attrs().MasterIndex)
			}
			addrs, e := netlink.AddrList(link, netlink.FAMILY_V4)
			if e != nil {
				return fmt.Errorf("listing %s addresses: %w", name, e)
			}
			if len(addrs) != 0 {
				t.Errorf("%s received addresses despite not being a workload port: %v", name, addrs)
			}
		}
		link, e := netlink.LinkByName(owned)
		if e != nil {
			return fmt.Errorf("looking up %s: %w", owned, e)
		}
		if link.Attrs().MasterIndex == 0 {
			t.Errorf("%s was not programmed although only the other entries were refused", owned)
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileL2AttachedPorts validates that a workload-CNI L2 attach port is
// enslaved to its Layer2 bridge (l2.<vlanID>) with no addressing.
func TestReconcileL2AttachedPorts(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const (
		port   = "cral201234567"
		vlanID = 100
	)
	bridgeName := fmt.Sprintf("l2.%d", vlanID)
	if derr := testNS.Do(func(_ ns.NetNS) error {
		br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: bridgeName}}
		if e := netlink.LinkAdd(br); e != nil {
			return fmt.Errorf("adding bridge: %w", e)
		}
		if e := netlink.LinkSetUp(br); e != nil {
			return fmt.Errorf("setting bridge up: %w", e)
		}
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		Layer2s: []Layer2Information{{
			VlanID: vlanID,
			AttachedPorts: []L2AttachedPort{{
				Interface: port,
			}},
		}},
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts: %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		link, e := netlink.LinkByName(port)
		if e != nil {
			t.Errorf("port %s missing: %v", port, e)
			return nil
		}
		brLink, _ := netlink.LinkByName(bridgeName)
		if link.Attrs().MasterIndex != brLink.Attrs().Index {
			t.Errorf("port not enslaved to bridge %s (master=%d, want %d)",
				bridgeName, link.Attrs().MasterIndex, brLink.Attrs().Index)
		}
		// L2 attach carries no L3 addressing.
		addrs, _ := netlink.AddrList(link, netlink.FAMILY_ALL)
		for i := range addrs {
			if addrs[i].IP.IsGlobalUnicast() {
				t.Errorf("L2 attach port unexpectedly has address %s", addrs[i].IPNet.String())
			}
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileL2AttachedPortsAdoptOnly ensures a missing L2 attach port is a
// no-op (the CNI owns the veth lifecycle).
func TestReconcileL2AttachedPortsAdoptOnly(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		Layer2s: []Layer2Information{{
			VlanID:        200,
			AttachedPorts: []L2AttachedPort{{Interface: "cramissingl2"}},
		}},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		// Missing port: must be a no-op, not an error.
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("adopt-only L2 reconcile: %v", derr)
	}
}

func assertHostRoutes(t *testing.T, table int, wantV4, wantV6 string) {
	t.Helper()
	routes, _ := netlink.RouteListFiltered(netlink.FAMILY_ALL,
		&netlink.Route{Table: table}, netlink.RT_FILTER_TABLE)
	var haveV4, haveV6 bool
	for i := range routes {
		if routes[i].Dst == nil {
			continue
		}
		switch routes[i].Dst.String() {
		case wantV4:
			haveV4 = true
			if routes[i].Protocol != unix.RTPROT_BOOT {
				t.Errorf("v4 route proto = %d, want RTPROT_BOOT (%d)", routes[i].Protocol, unix.RTPROT_BOOT)
			}
		case wantV6:
			haveV6 = true
			if routes[i].Protocol != unix.RTPROT_BOOT {
				t.Errorf("v6 route proto = %d, want RTPROT_BOOT (%d)", routes[i].Protocol, unix.RTPROT_BOOT)
			}
		}
	}
	if !haveV4 {
		t.Errorf("missing on-link route %s in table %d", wantV4, table)
	}
	if !haveV6 {
		t.Errorf("missing on-link route %s in table %d", wantV6, table)
	}
}

// TestReconcileL2TrunkPorts covers a two-member trunk: the raw port stays
// unenslaved and each member is reached through its own vlan sub-interface,
// including a translated one whose workload-side id differs from the domain's.
func TestReconcileL2TrunkPorts(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const port = "cratrunk0"
	bridges := []int{100, 200}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		for _, vlanID := range bridges {
			br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: fmt.Sprintf("l2.%d", vlanID)}}
			if e := netlink.LinkAdd(br); e != nil {
				return fmt.Errorf("adding bridge: %w", e)
			}
			if e := netlink.LinkSetUp(br); e != nil {
				return fmt.Errorf("setting bridge up: %w", e)
			}
		}
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	// The port starts out as an untagged access member of one of the domains, so
	// the trunk reconcile has to take it out of that bridge again: an enslaved
	// parent would keep leaking untagged and unmapped-tag frames into it.
	access := &NetlinkConfiguration{
		Layer2s: []Layer2Information{
			{VlanID: 100, MTU: 9000, AttachedPorts: []L2AttachedPort{{Interface: port}}},
		},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(access)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts (access): %v", derr)
	}

	cfg := &NetlinkConfiguration{
		Layer2s: []Layer2Information{
			// Inherited: the domain's own id is also the workload-side id.
			{VlanID: 100, MTU: 9000, AttachedPorts: []L2AttachedPort{{Interface: port, VlanID: 100}}},
			// Translated: fabric-side 200 carried as 3000 on the workload side.
			{VlanID: 200, MTU: 9000, AttachedPorts: []L2AttachedPort{{Interface: port, VlanID: 3000}}},
		},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts: %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		parent, e := netlink.LinkByName(port)
		if e != nil {
			t.Errorf("port %s missing: %v", port, e)
			return nil
		}
		// A trunked port must never be a bridge slave itself, or untagged and
		// unlisted tags would leak into whichever domain it was enslaved to.
		if parent.Attrs().MasterIndex != 0 {
			t.Errorf("trunked port unexpectedly enslaved (master=%d)", parent.Attrs().MasterIndex)
		}

		for _, tc := range []struct{ subVlan, bridgeVlan int }{{100, 100}, {3000, 200}} {
			name := fmt.Sprintf("%s.%d", port, tc.subVlan)
			link, lerr := netlink.LinkByName(name)
			if lerr != nil {
				t.Errorf("sub-interface %s missing: %v", name, lerr)
				continue
			}
			vlan, ok := link.(*netlink.Vlan)
			if !ok || vlan.VlanId != tc.subVlan || vlan.ParentIndex != parent.Attrs().Index {
				t.Errorf("sub-interface %s = %+v, want vlan %d on %s", name, link, tc.subVlan, port)
				continue
			}
			brLink, _ := netlink.LinkByName(fmt.Sprintf("l2.%d", tc.bridgeVlan))
			if vlan.MasterIndex != brLink.Attrs().Index {
				t.Errorf("sub-interface %s not enslaved to l2.%d (master=%d, want %d)",
					name, tc.bridgeVlan, vlan.MasterIndex, brLink.Attrs().Index)
			}
			if vlan.MTU != parent.Attrs().MTU {
				t.Errorf("sub-interface %s MTU = %d, want the port's %d", name, vlan.MTU, parent.Attrs().MTU)
			}
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileL2TrunkRemovesStaleSubinterfaces covers a member being dropped
// while the port stays up: its sub-interface must go, or it would stay bridged
// into the domain it was just detached from.
func TestReconcileL2TrunkRemovesStaleSubinterfaces(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const port = "cratrunk1"
	if derr := testNS.Do(func(_ ns.NetNS) error {
		for _, vlanID := range []int{100, 200} {
			br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: fmt.Sprintf("l2.%d", vlanID)}}
			if e := netlink.LinkAdd(br); e != nil {
				return fmt.Errorf("adding bridge: %w", e)
			}
		}
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		Layer2s: []Layer2Information{
			{VlanID: 100, AttachedPorts: []L2AttachedPort{{Interface: port, VlanID: 100}}},
			{VlanID: 200, AttachedPorts: []L2AttachedPort{{Interface: port, VlanID: 3000}}},
		},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts: %v", derr)
	}

	// Drop the second member and reconcile again.
	cfg.Layer2s = cfg.Layer2s[:1]
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts (shrunk trunk): %v", derr)
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		if _, e := netlink.LinkByName(port + ".3000"); e == nil {
			t.Errorf("stale sub-interface %s.3000 was not removed", port)
		}
		if _, e := netlink.LinkByName(port + ".100"); e != nil {
			t.Errorf("live sub-interface %s.100 was removed: %v", port, e)
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileL2TrunkRemovesSubinterfacesOfDroppedPort covers a port losing
// its last trunk member — because the attachment was dropped as a whole (an
// all-or-nothing merge drop, with the workload still running) or because it
// turned into an access port. Neither leaves the port in the desired set, so
// ownership has to be recognised on the datapath itself; a vlan sub-interface
// that is not enslaved to a Layer2 bridge is none of the reconciler's business.
func TestReconcileL2TrunkRemovesSubinterfacesOfDroppedPort(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const (
		dropped  = "cratrunk2"
		toAccess = "cratrunk3"
		foreign  = "notcra0"
	)
	if derr := testNS.Do(func(_ ns.NetNS) error {
		for _, vlanID := range []int{100, 200} {
			br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: fmt.Sprintf("l2.%d", vlanID)}}
			if e := netlink.LinkAdd(br); e != nil {
				return fmt.Errorf("adding bridge: %w", e)
			}
		}
		for _, name := range []string{dropped, toAccess} {
			if e := addWorkloadVethPort(name); e != nil {
				return e
			}
		}
		// A same-shaped vlan sub-interface on a veth that is not a workload
		// port and that nothing enslaved into a Layer2 domain: not ours, must
		// survive.
		if e := addVethPort(foreign); e != nil {
			return e
		}
		fLink, e := netlink.LinkByName(foreign)
		if e != nil {
			return fmt.Errorf("looking up %s: %w", foreign, e)
		}
		attrs := netlink.NewLinkAttrs()
		attrs.Name = foreign + ".100"
		attrs.ParentIndex = fLink.Attrs().Index
		return netlink.LinkAdd(&netlink.Vlan{LinkAttrs: attrs, VlanId: 100})
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		Layer2s: []Layer2Information{
			{VlanID: 100, AttachedPorts: []L2AttachedPort{
				{Interface: dropped, VlanID: 100},
				{Interface: toAccess, VlanID: 100},
			}},
			{VlanID: 200, AttachedPorts: []L2AttachedPort{{Interface: dropped, VlanID: 3000}}},
		},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts: %v", derr)
	}

	// The first port's attachment goes away entirely; the second becomes an
	// untagged access member of the same domain it trunked before.
	cfg.Layer2s = []Layer2Information{
		{VlanID: 100, AttachedPorts: []L2AttachedPort{{Interface: toAccess}}},
		{VlanID: 200},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts (dropped trunk): %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		for _, name := range []string{dropped + ".100", dropped + ".3000", toAccess + ".100"} {
			if _, e := netlink.LinkByName(name); e == nil {
				t.Errorf("stale sub-interface %s was not removed", name)
			}
		}
		if _, e := netlink.LinkByName(foreign + ".100"); e != nil {
			t.Errorf("unrelated sub-interface %s.100 was removed: %v", foreign, e)
		}
		link, e := netlink.LinkByName(toAccess)
		if e != nil {
			t.Fatalf("port %s missing: %v", toAccess, e)
		}
		br, e := netlink.LinkByName("l2.100")
		if e != nil {
			t.Fatalf("bridge l2.100 missing: %v", e)
		}
		if link.Attrs().MasterIndex != br.Attrs().Index {
			t.Errorf("access port %s not enslaved to l2.100 (master=%d, want %d)",
				toAccess, link.Attrs().MasterIndex, br.Attrs().Index)
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileL2TrunkRemovesOrphanedSubinterfaces covers the domain-removal
// order: the Layer2 reconciler deletes the bridge before this cleanup sees the
// sub-interface, and the attachment is dropped from the configuration as a
// whole, so neither the master nor the desired set identifies the link. The
// parent's workload-port alias does; a same-shaped sub-interface on a veth
// without it (the CRA's own trunk, say) is not touched, whether or not it sits
// in one of the domain bridges.
func TestReconcileL2TrunkRemovesOrphanedSubinterfaces(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const (
		port    = "cratrunk4"
		foreign = "hbnx0"
	)
	if derr := testNS.Do(func(_ ns.NetNS) error {
		br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "l2.100"}}
		if e := netlink.LinkAdd(br); e != nil {
			return fmt.Errorf("adding bridge: %w", e)
		}
		if e := addWorkloadVethPort(port); e != nil {
			return e
		}
		if e := addVethPort(foreign); e != nil {
			return e
		}
		fLink, e := netlink.LinkByName(foreign)
		if e != nil {
			return fmt.Errorf("looking up %s: %w", foreign, e)
		}
		// Even enslaved into the domain's bridge by someone else: bridge
		// membership is not what makes a sub-interface ours.
		attrs := netlink.NewLinkAttrs()
		attrs.Name = foreign + ".100"
		attrs.ParentIndex = fLink.Attrs().Index
		attrs.MasterIndex = br.Attrs().Index
		return netlink.LinkAdd(&netlink.Vlan{LinkAttrs: attrs, VlanId: 100})
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		Layer2s: []Layer2Information{
			{VlanID: 100, AttachedPorts: []L2AttachedPort{{Interface: port, VlanID: 100}}},
		},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts: %v", derr)
	}

	// The domain disappears: its bridge is gone before the cleanup runs and the
	// attachment that referenced it is no longer part of the configuration.
	if derr := testNS.Do(func(_ ns.NetNS) error {
		br, e := netlink.LinkByName("l2.100")
		if e != nil {
			return fmt.Errorf("looking up bridge: %w", e)
		}
		return netlink.LinkDel(br)
	}); derr != nil {
		t.Fatalf("deleting bridge: %v", derr)
	}
	cfg.Layer2s = nil
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts (dropped domain): %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		if _, e := netlink.LinkByName(port + ".100"); e == nil {
			t.Errorf("orphaned sub-interface %s.100 was not removed", port)
		}
		if _, e := netlink.LinkByName(port); e != nil {
			t.Errorf("workload port %s itself was removed: %v", port, e)
		}
		if _, e := netlink.LinkByName(foreign + ".100"); e != nil {
			t.Errorf("unrelated sub-interface %s.100 was removed: %v", foreign, e)
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestReconcileL2AccessPortsReleaseStale ensures an access port whose attachment
// went away (or moved to a domain that is not present) is taken out of its
// bridge while the port itself, the desired members and the bridge's own
// infrastructure members are left alone.
func TestReconcileL2AccessPortsReleaseStale(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const (
		dropped = "cradrop000001"
		kept    = "crakeep000001"
		moved   = "cramove000001"
		infra   = "vx.100100"
		foreign = "notcra1" // a veth this CNI did not create, enslaved by someone else
	)
	if derr := testNS.Do(func(_ ns.NetNS) error {
		br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "l2.100"}}
		if e := netlink.LinkAdd(br); e != nil {
			return fmt.Errorf("adding bridge: %w", e)
		}
		for _, name := range []string{dropped, kept, moved} {
			if e := addWorkloadVethPort(name); e != nil {
				return e
			}
		}
		if e := addEnslavedVethPort(foreign, br); e != nil {
			return e
		}
		// The domain's VXLAN device is a bridge member too, but not a workload
		// port: it must never be touched.
		vx := &netlink.Vxlan{
			LinkAttrs: netlink.LinkAttrs{Name: infra, MasterIndex: br.Attrs().Index},
			VxlanId:   100100,
			Port:      4789,
			Learning:  false,
		}
		return netlink.LinkAdd(vx)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		Layer2s: []Layer2Information{{VlanID: 100, AttachedPorts: []L2AttachedPort{
			{Interface: dropped}, {Interface: kept}, {Interface: moved},
		}}},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts: %v", derr)
	}

	// One attachment is dropped altogether, another is re-pointed at a domain
	// whose bridge is not on the node: neither may stay in l2.100.
	cfg.Layer2s = []Layer2Information{
		{VlanID: 100, AttachedPorts: []L2AttachedPort{{Interface: kept}}},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts (dropped access): %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		br, e := netlink.LinkByName("l2.100")
		if e != nil {
			t.Fatalf("bridge l2.100 missing: %v", e)
		}
		for _, name := range []string{dropped, moved} {
			link, e := netlink.LinkByName(name)
			if e != nil {
				t.Errorf("stale port %s must be left in place (the CNI owns it): %v", name, e)
				continue
			}
			if link.Attrs().MasterIndex != 0 {
				t.Errorf("stale port %s still enslaved (master=%d)", name, link.Attrs().MasterIndex)
			}
		}
		for _, name := range []string{kept, infra, foreign} {
			link, e := netlink.LinkByName(name)
			if e != nil {
				t.Fatalf("%s missing: %v", name, e)
			}
			if link.Attrs().MasterIndex != br.Attrs().Index {
				t.Errorf("%s not (or no longer) enslaved to l2.100 (master=%d)", name, link.Attrs().MasterIndex)
			}
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

func TestReconcileL2AccessPortMovesBetweenBridges(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const port = "cramove000002"
	if derr := testNS.Do(func(_ ns.NetNS) error {
		for _, name := range []string{"l2.100", "l2.200"} {
			if e := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: name}}); e != nil {
				return fmt.Errorf("adding bridge %s: %w", name, e)
			}
		}
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		Layer2s: []Layer2Information{{VlanID: 100, AttachedPorts: []L2AttachedPort{{Interface: port}}}},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts: %v", derr)
	}

	// Re-pointing the attachment at another domain while the port is still
	// enslaved in the old bridge must swap masters, not fail with EBUSY.
	cfg.Layer2s = []Layer2Information{{VlanID: 200, AttachedPorts: []L2AttachedPort{{Interface: port}}}}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts (moved): %v", derr)
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		br, e := netlink.LinkByName("l2.200")
		if e != nil {
			return fmt.Errorf("bridge l2.200 missing: %w", e)
		}
		link, e := netlink.LinkByName(port)
		if e != nil {
			return fmt.Errorf("port %s missing: %w", port, e)
		}
		if link.Attrs().MasterIndex != br.Attrs().Index {
			t.Errorf("%s not enslaved to l2.200 (master=%d, want %d)", port, link.Attrs().MasterIndex, br.Attrs().Index)
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// TestWorkloadPortSwitchesBetweenBridgeAndVRF runs the reconcilers in the order
// the agent does (routed ports first, then L2 attached ports) against a port
// that is still enslaved in the previous mode's master. Bridge -> VRF must not
// fail with EBUSY: IFLA_MASTER makes the kernel detach the port from its bridge
// (ndo_del_slave) before the VRF adopts it. VRF -> bridge releases the VRF
// master explicitly before enslaving.
func TestWorkloadPortSwitchesBetweenBridgeAndVRF(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const port = "cramove000003"
	if derr := testNS.Do(func(_ ns.NetNS) error {
		if e := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "l2.100"}}); e != nil {
			return fmt.Errorf("adding bridge: %w", e)
		}
		vrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "tenant-a"}, Table: routedTestVRFTable}
		if e := netlink.LinkAdd(vrf); e != nil {
			return fmt.Errorf("adding vrf: %w", e)
		}
		if e := netlink.LinkSetUp(vrf); e != nil {
			return fmt.Errorf("setting vrf up: %w", e)
		}
		return addWorkloadVethPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	routes := []string{"10.100.0.12/32", "fd00:100::12/128"}
	asL2 := &NetlinkConfiguration{
		Layer2s: []Layer2Information{{VlanID: 100, AttachedPorts: []L2AttachedPort{{Interface: port}}}},
	}
	asRouted := &NetlinkConfiguration{
		Layer2s: []Layer2Information{{VlanID: 100}},
		WorkloadPorts: []WorkloadPort{{
			Interface: port, VRF: "tenant-a", GatewayV4: "169.254.1.1/32", GatewayV6: "fe80::1/128", HostRoutes: routes,
		}},
	}
	reconcile := func(cfg *NetlinkConfiguration) error {
		if e := mgr.ReconcileWorkloadPorts(cfg); e != nil {
			return fmt.Errorf("ReconcileWorkloadPorts: %w", e)
		}
		if e := mgr.ReconcileL2AttachedPorts(cfg); e != nil {
			return fmt.Errorf("ReconcileL2AttachedPorts: %w", e)
		}
		return nil
	}
	masterOf := func(name string) (int, error) {
		link, e := netlink.LinkByName(name)
		if e != nil {
			return 0, fmt.Errorf("looking up %s: %w", name, e)
		}
		return link.Attrs().Index, nil
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		if e := reconcile(asL2); e != nil {
			return fmt.Errorf("initial L2 reconcile: %w", e)
		}
		if e := reconcile(asRouted); e != nil {
			return fmt.Errorf("bridge -> VRF reconcile: %w", e)
		}
		link, e := netlink.LinkByName(port)
		if e != nil {
			return fmt.Errorf("looking up port: %w", e)
		}
		vrfIdx, e := masterOf("tenant-a")
		if e != nil {
			return e
		}
		if link.Attrs().MasterIndex != vrfIdx {
			t.Errorf("after bridge -> VRF: master=%d, want VRF %d", link.Attrs().MasterIndex, vrfIdx)
		}
		assertHostRoutes(t, routedTestVRFTable, routes[0], routes[1])

		if e := reconcile(asL2); e != nil {
			return fmt.Errorf("VRF -> bridge reconcile: %w", e)
		}
		if link, e = netlink.LinkByName(port); e != nil {
			return fmt.Errorf("looking up port: %w", e)
		}
		brIdx, e := masterOf("l2.100")
		if e != nil {
			return e
		}
		if link.Attrs().MasterIndex != brIdx {
			t.Errorf("after VRF -> bridge: master=%d, want bridge %d", link.Attrs().MasterIndex, brIdx)
		}
		return assertNoRoutedState(t, link)
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

// assertNoRoutedState checks that nothing of the routed mode survives on a
// bridged port: the gateway addresses would answer ARP/ND inside the domain and
// the host routes keep steering the old addresses into it.
func assertNoRoutedState(t *testing.T, link netlink.Link) error {
	t.Helper()
	addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("listing port addresses: %w", err)
	}
	for i := range addrs {
		if ones, bits := addrs[i].Mask.Size(); ones == bits {
			t.Errorf("gateway address %s still on the bridged port", addrs[i].IPNet)
		}
	}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_ALL,
		&netlink.Route{LinkIndex: link.Attrs().Index, Table: unix.RT_TABLE_UNSPEC},
		netlink.RT_FILTER_OIF|netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("listing port routes: %w", err)
	}
	for i := range routes {
		if routes[i].Protocol == unix.RTPROT_BOOT && routes[i].Dst != nil {
			t.Errorf("host route %s (table %d) still on the bridged port", routes[i].Dst, routes[i].Table)
		}
	}
	return nil
}

// TestReconcileL2AttachedPortsRefusesForeignLinks mirrors the routed check for
// the L2 mode: an entry naming a link that is not a CNI-stamped veth must not
// get it bridged into the domain, while the owned port of the same
// configuration is still attached.
func TestReconcileL2AttachedPortsRefusesForeignLinks(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const (
		platform = "hbn"           // a dummy standing in for the trunk
		unowned  = "cral2strang01" // a veth without the CNI alias
		owned    = "cral2owned001"
	)
	if derr := testNS.Do(func(_ ns.NetNS) error {
		if e := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "l2.100"}}); e != nil {
			return fmt.Errorf("adding bridge: %w", e)
		}
		if e := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: platform}}); e != nil {
			return fmt.Errorf("adding platform link: %w", e)
		}
		if e := addVethPort(unowned); e != nil {
			return e
		}
		return addWorkloadVethPort(owned)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{Layer2s: []Layer2Information{{
		VlanID:        100,
		AttachedPorts: []L2AttachedPort{{Interface: platform}, {Interface: unowned, VlanID: 200}, {Interface: owned}},
	}}}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		err := mgr.ReconcileL2AttachedPorts(cfg)
		if err == nil {
			t.Fatal("expected the foreign links to be refused")
		}
		for _, name := range []string{platform, unowned} {
			if !strings.Contains(err.Error(), fmt.Sprintf("port %q", name)) {
				t.Errorf("error does not name refused link %q: %v", name, err)
			}
			link, e := netlink.LinkByName(name)
			if e != nil {
				return fmt.Errorf("looking up %s: %w", name, e)
			}
			if link.Attrs().MasterIndex != 0 {
				t.Errorf("%s was enslaved (master=%d) despite not being a workload port", name, link.Attrs().MasterIndex)
			}
		}
		if _, e := netlink.LinkByName(unowned + ".200"); e == nil {
			t.Errorf("a trunk sub-interface was created on %s despite it not being a workload port", unowned)
		}
		link, e := netlink.LinkByName(owned)
		if e != nil {
			return fmt.Errorf("looking up %s: %w", owned, e)
		}
		if link.Attrs().MasterIndex == 0 {
			t.Errorf("%s was not bridged although only the other entries were refused", owned)
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}

func TestReconcileL2AttachedPortsMissingBridgeStillReleases(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	const (
		moved = "cramove000003"
		kept  = "crakeep000003"
	)
	if derr := testNS.Do(func(_ ns.NetNS) error {
		if e := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "l2.100"}}); e != nil {
			return fmt.Errorf("adding bridge: %w", e)
		}
		for _, name := range []string{moved, kept} {
			if e := addWorkloadVethPort(name); e != nil {
				return e
			}
		}
		return nil
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		Layer2s: []Layer2Information{{VlanID: 100, AttachedPorts: []L2AttachedPort{
			{Interface: moved}, {Interface: kept},
		}}},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		return mgr.ReconcileL2AttachedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileL2AttachedPorts: %v", derr)
	}

	// The attachment moves to a domain whose bridge does not exist (yet). The
	// error must surface, but not before the port was released from l2.100 and
	// the remaining member was still programmed.
	cfg.Layer2s = []Layer2Information{
		{VlanID: 100, AttachedPorts: []L2AttachedPort{{Interface: kept}}},
		{VlanID: 999, AttachedPorts: []L2AttachedPort{{Interface: moved}}},
	}
	var rerr error
	if derr := testNS.Do(func(_ ns.NetNS) error {
		rerr = mgr.ReconcileL2AttachedPorts(cfg)
		return nil
	}); derr != nil {
		t.Fatalf("netns do: %v", derr)
	}
	if rerr == nil {
		t.Fatalf("expected an error for the missing bridge l2.999")
	}

	if derr := testNS.Do(func(_ ns.NetNS) error {
		br, e := netlink.LinkByName("l2.100")
		if e != nil {
			return fmt.Errorf("bridge l2.100 missing: %w", e)
		}
		link, e := netlink.LinkByName(moved)
		if e != nil {
			return fmt.Errorf("port %s missing: %w", moved, e)
		}
		if link.Attrs().MasterIndex != 0 {
			t.Errorf("%s still enslaved (master=%d) although its domain is gone", moved, link.Attrs().MasterIndex)
		}
		link, e = netlink.LinkByName(kept)
		if e != nil {
			return fmt.Errorf("port %s missing: %w", kept, e)
		}
		if link.Attrs().MasterIndex != br.Attrs().Index {
			t.Errorf("%s not enslaved to l2.100 (master=%d)", kept, link.Attrs().MasterIndex)
		}
		return nil
	}); derr != nil {
		t.Fatalf("netns check: %v", derr)
	}
}
