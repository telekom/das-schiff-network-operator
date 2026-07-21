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
	return netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}})
}

func TestReconcileRoutedPortsVRF(t *testing.T) {
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
			return e
		}
		if e := netlink.LinkSetUp(vrf); e != nil {
			return e
		}
		return addDummyPort(port)
	}); derr != nil {
		t.Fatalf("populate netns: %v", derr)
	}

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		RoutedPorts: []RoutedPort{{
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
		return mgr.ReconcileRoutedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileRoutedPorts: %v", derr)
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

func TestReconcileRoutedPortsUnderlayMainTable(t *testing.T) {
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
		RoutedPorts: []RoutedPort{{
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
		return mgr.ReconcileRoutedPorts(cfg)
	}); derr != nil {
		t.Fatalf("ReconcileRoutedPorts: %v", derr)
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

// TestReconcileRoutedPortsAdoptOnly ensures a missing port is skipped without an
// error (the CNI owns the veth lifecycle) and that reconciliation is idempotent.
func TestReconcileRoutedPortsAdoptOnly(t *testing.T) {
	requireRoot(t)

	testNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create netns: %v", err)
	}
	defer testutils.UnmountNS(testNS) //nolint:errcheck

	mgr := NewManager(&Toolkit{}, nil)
	cfg := &NetlinkConfiguration{
		RoutedPorts: []RoutedPort{{
			Interface:  "cramissing000",
			HostRoutes: []string{"10.10.0.1/32"},
		}},
	}
	if derr := testNS.Do(func(_ ns.NetNS) error {
		// Missing port: must be a no-op, not an error.
		if e := mgr.ReconcileRoutedPorts(cfg); e != nil {
			return e
		}
		// Add the port and reconcile twice to confirm idempotency.
		if e := addDummyPort("craidem000001"); e != nil {
			return e
		}
		cfg.RoutedPorts[0].Interface = "craidem000001"
		cfg.RoutedPorts[0].GatewayV4 = "169.254.9.9/32"
		if e := mgr.ReconcileRoutedPorts(cfg); e != nil {
			return e
		}
		return mgr.ReconcileRoutedPorts(cfg)
	}); derr != nil {
		t.Fatalf("adopt-only reconcile: %v", derr)
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
