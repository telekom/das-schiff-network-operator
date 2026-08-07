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
