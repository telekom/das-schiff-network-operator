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

// addDummyPort creates a dummy netdev that stands in for the CNI-moved veth end.
func addDummyPort(name string) error {
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
		return fmt.Errorf("adding dummy port: %w", err)
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

	const port = "cra0123456789ab"
	if derr := testNS.Do(func(_ ns.NetNS) error {
		vrf := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "cluster"}, Table: routedTestVRFTable}
		if e := netlink.LinkAdd(vrf); e != nil {
			return fmt.Errorf("adding vrf: %w", e)
		}
		if e := netlink.LinkSetUp(vrf); e != nil {
			return fmt.Errorf("setting vrf up: %w", e)
		}
		return addDummyPort(port)
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
		return addDummyPort(port)
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
		if e := addDummyPort("craidem000001"); e != nil {
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
		return addDummyPort(port)
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
		return addDummyPort(port)
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
		return addDummyPort(port)
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
		for _, name := range []string{dropped, toAccess, foreign} {
			if e := addDummyPort(name); e != nil {
				return e
			}
		}
		// A same-shaped vlan sub-interface that nothing enslaved into a Layer2
		// domain: not ours, must survive.
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
