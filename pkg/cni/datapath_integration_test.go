//go:build linux

/*
Copyright 2024.

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

package cni

import (
	"net"
	"os"
	"testing"

	"github.com/containernetworking/cni/pkg/skel"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/containernetworking/plugins/pkg/testutils"
	"github.com/vishvananda/netlink"
)

// requireRoot skips the test when not running as root (netns creation and
// netlink writes need CAP_NET_ADMIN in the initial user namespace).
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("integration test requires root (netns + netlink)")
	}
}

const testVRFTable = 1234

// newCRANetns creates a fresh, bind-mounted netns (under /var/run/netns)
// containing a "cluster" VRF (table 1234) and a dummy trunk interface named
// trunk (so auto-discovery can find it).
func newCRANetns(t *testing.T, trunk string) ns.NetNS {
	t.Helper()
	craNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create CRA netns: %v", err)
	}
	err = craNS.Do(func(_ ns.NetNS) error {
		vrf := &netlink.Vrf{
			LinkAttrs: netlink.LinkAttrs{Name: "cluster"},
			Table:     testVRFTable,
		}
		if e := netlink.LinkAdd(vrf); e != nil {
			return e
		}
		if e := netlink.LinkSetUp(vrf); e != nil {
			return e
		}
		hbn := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: trunk}}
		return netlink.LinkAdd(hbn)
	})
	if err != nil {
		_ = testutils.UnmountNS(craNS)
		t.Fatalf("populate CRA netns: %v", err)
	}
	return craNS
}

// TestDatapathAddDel exercises the flavor-agnostic CNI datapath: create the
// veth pair, assign the pod-side addresses, move the CRA-side port into the CRA
// netns and bring it up. ALL CRA-side FIB programming (VRF enslave, on-link
// gateways, host routes) is now performed by the node-local agent (see
// pkg/nl.ReconcileRoutedPorts), NOT the plugin, so this test only asserts the
// veth wiring and the moved-port state.
func TestDatapathAddDel(t *testing.T) {
	requireRoot(t)

	craNS := newCRANetns(t, "hbn")
	defer testutils.UnmountNS(craNS) //nolint:errcheck
	podNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create pod netns: %v", err)
	}
	defer testutils.UnmountNS(podNS) //nolint:errcheck

	conf := &NetConf{VRF: "cluster", CRANetns: craNS.Path()}
	args := &skel.CmdArgs{
		ContainerID: "integration-test-container",
		Netns:       podNS.Path(),
		IfName:      "net1",
	}
	result := &current.Result{
		IPs: []*current.IPConfig{
			{Address: net.IPNet{IP: net.ParseIP("10.100.0.5"), Mask: net.CIDRMask(24, 32)}},
			{Address: net.IPNet{IP: net.ParseIP("fd00:100::5"), Mask: net.CIDRMask(64, 128)}},
		},
	}
	port := portName(args.ContainerID)

	if _, err := setupPodSide(conf, args, craNS.Path(), port, result); err != nil {
		t.Fatalf("setupPodSide: %v", err)
	}
	if _, err := setupCRASide(craNS.Path(), port); err != nil {
		t.Fatalf("setupCRASide: %v", err)
	}

	// Pod side: veth "net1" exists and carries the allocated addresses.
	if derr := podNS.Do(func(_ ns.NetNS) error {
		link, e := netlink.LinkByName("net1")
		if e != nil {
			t.Errorf("pod veth net1 missing: %v", e)
			return nil
		}
		addrs, _ := netlink.AddrList(link, netlink.FAMILY_ALL)
		var haveV4 bool
		for _, a := range addrs {
			if a.IP.Equal(net.ParseIP("10.100.0.5")) {
				haveV4 = true
			}
		}
		if !haveV4 {
			t.Errorf("pod veth net1 missing IPv4 address 10.100.0.5")
		}
		return nil
	}); derr != nil {
		t.Fatalf("pod netns check: %v", derr)
	}

	// CRA side: the moved port exists and is up. FIB programming is the agent's
	// job and is validated in pkg/nl (TestReconcileRoutedPorts).
	if derr := craNS.Do(func(_ ns.NetNS) error {
		link, e := netlink.LinkByName(port)
		if e != nil {
			t.Errorf("CRA port %s missing: %v", port, e)
			return nil
		}
		if link.Attrs().Flags&net.FlagUp == 0 {
			t.Errorf("CRA port %s is not up", port)
		}
		return nil
	}); derr != nil {
		t.Fatalf("CRA netns check: %v", derr)
	}

	// Teardown removes both ends.
	if err := teardownCRASide(craNS.Path(), port); err != nil {
		t.Errorf("teardownCRASide: %v", err)
	}
	if err := teardownPodSide(podNS.Path(), "net1"); err != nil {
		t.Errorf("teardownPodSide: %v", err)
	}
	if derr := podNS.Do(func(_ ns.NetNS) error {
		if _, e := netlink.LinkByName("net1"); e == nil {
			t.Errorf("pod veth net1 still present after teardown")
		}
		return nil
	}); derr != nil {
		t.Fatalf("post-teardown pod check: %v", derr)
	}
}

func TestDiscoverCRANetnsByTrunk(t *testing.T) {
	requireRoot(t)

	// Use a unique trunk name so auto-discovery matches only this netns even if
	// other test namespaces are still present.
	craNS := newCRANetns(t, "hbndisco0")
	defer testutils.UnmountNS(craNS) //nolint:errcheck

	conf := &NetConf{VRF: "cluster", CRANetns: "auto", TrunkInterface: "hbndisco0"}
	got, err := resolveCRANetnsPath(conf)
	if err != nil {
		t.Fatalf("resolveCRANetnsPath(auto): %v", err)
	}
	if got != craNS.Path() {
		t.Errorf("resolveCRANetnsPath(auto) = %q, want %q", got, craNS.Path())
	}
}
