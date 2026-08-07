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
	"errors"
	"fmt"
	"net"
	"os"
	"testing"

	"github.com/containernetworking/cni/pkg/skel"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/containernetworking/plugins/pkg/testutils"
	"github.com/vishvananda/netlink"

	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
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
			return fmt.Errorf("adding vrf: %w", e)
		}
		if e := netlink.LinkSetUp(vrf); e != nil {
			return fmt.Errorf("setting vrf up: %w", e)
		}
		hbn := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: trunk}}
		if e := netlink.LinkAdd(hbn); e != nil {
			return fmt.Errorf("adding trunk dummy: %w", e)
		}
		return nil
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
// pkg/nl.ReconcileWorkloadPorts), NOT the plugin, so this test only asserts the
// veth wiring and the moved-port state.
func TestDatapathAddDel(t *testing.T) {
	requireRoot(t)

	// The CRA netns has to own the peer of a host-side trunk veth: setupPodSide
	// re-verifies that on the very handle it moves the port through.
	const trunk = "hbnadddel0"
	craNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create CRA netns: %v", err)
	}
	defer testutils.UnmountNS(craNS) //nolint:errcheck
	connectTrunk(t, craNS, trunk)
	podNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create pod netns: %v", err)
	}
	defer testutils.UnmountNS(podNS) //nolint:errcheck

	conf := &NetConf{VRF: "cluster", NodeConfig: NodeConfig{CRANetns: craNS.Path(), TrunkInterface: trunk}}
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
	port := portName(args.ContainerID, args.IfName)

	verifiedNS, err := openCRANetns(conf, craNS.Path())
	if err != nil {
		t.Fatalf("openCRANetns: %v", err)
	}
	defer verifiedNS.Close()
	if _, err := setupPodSide(conf, args, verifiedNS, port, result); err != nil {
		t.Fatalf("setupPodSide: %v", err)
	}
	if _, err := setupCRASide(verifiedNS, port); err != nil {
		t.Fatalf("setupCRASide: %v", err)
	}

	// Pod side: veth "net1" exists and carries the allocated addresses.
	if derr := podNS.Do(func(_ ns.NetNS) error {
		assertLinkAddrs(t, "net1", "10.100.0.5", "fd00:100::5")
		return nil
	}); derr != nil {
		t.Fatalf("pod netns check: %v", derr)
	}

	// CRA side: the moved port exists and is up. FIB programming is the agent's
	// job and is validated in pkg/nl (TestReconcileWorkloadPorts).
	if derr := craNS.Do(func(_ ns.NetNS) error {
		link, e := netlink.LinkByName(port)
		if e != nil {
			t.Errorf("CRA port %s missing: %v", port, e)
			return nil
		}
		if link.Attrs().Flags&net.FlagUp == 0 {
			t.Errorf("CRA port %s is not up", port)
		}
		if alias := link.Attrs().Alias; alias != workloadcni.InfraPortPrefix+port {
			t.Errorf("CRA port alias = %q, want %q", alias, workloadcni.InfraPortPrefix+port)
		}
		return nil
	}); derr != nil {
		t.Fatalf("CRA netns check: %v", derr)
	}

	// Teardown removes both ends.
	if err := teardownCRASide(verifiedNS, port); err != nil {
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

	// A repeated DEL (link already gone) and a DEL after the runtime destroyed
	// the netns must both succeed.
	if err := teardownPodSide(podNS.Path(), "net1"); err != nil {
		t.Errorf("repeated teardownPodSide: %v", err)
	}
	if err := teardownCRASide(verifiedNS, port); err != nil {
		t.Errorf("repeated teardownCRASide: %v", err)
	}
	if err := teardownPodSide("/var/run/netns/does-not-exist-"+args.ContainerID, "net1"); err != nil {
		t.Errorf("teardownPodSide on missing netns: %v", err)
	}
	if _, err := openCRANetns(conf, "/var/run/netns/does-not-exist-"+args.ContainerID); !errors.As(err, new(ns.NSPathNotExistErr)) {
		t.Errorf("openCRANetns on missing netns: got %v, want NSPathNotExistErr", err)
	}
}

// TestOpenCRANetnsRefusesUnverifiedNetns pins the CRA netns check to the handle
// every CRA-side step then runs through: a namespace path that resolves fine but
// does not own the trunk peer (here: a second netns merely carrying an
// interface named like the trunk, as a pod could set up through a Multus
// annotation) must be refused, regardless of what an earlier path-based
// resolution concluded.
func TestOpenCRANetnsRefusesUnverifiedNetns(t *testing.T) {
	requireRoot(t)

	const trunk = "hbnrefuse0"
	craNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create CRA netns: %v", err)
	}
	defer testutils.UnmountNS(craNS) //nolint:errcheck
	connectTrunk(t, craNS, trunk)
	strangerNS := newCRANetns(t, trunk)
	defer testutils.UnmountNS(strangerNS) //nolint:errcheck

	conf := &NetConf{NodeConfig: NodeConfig{TrunkInterface: trunk}}
	if _, err := openCRANetns(conf, strangerNS.Path()); err == nil {
		t.Fatal("openCRANetns accepted a netns that does not own the trunk peer")
	}
	verified, err := openCRANetns(conf, craNS.Path())
	if err != nil {
		t.Fatalf("openCRANetns refused the CRA netns: %v", err)
	}
	defer verified.Close()
	if verified.Path() != craNS.Path() {
		t.Errorf("openCRANetns returned %q, want %q", verified.Path(), craNS.Path())
	}
}

// assertLinkAddrs checks that the named link in the current netns carries every
// one of the given addresses.
func assertLinkAddrs(t *testing.T, name string, want ...string) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Errorf("link %s missing: %v", name, err)
		return
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		t.Errorf("listing addresses of %s: %v", name, err)
		return
	}
	for _, w := range want {
		found := false
		for _, a := range addrs {
			if a.IP.Equal(net.ParseIP(w)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("link %s missing address %s", name, w)
		}
	}
}

// connectTrunk creates the trunk veth pair the way the node is wired: the
// node-side end stays in the current (host) netns, its peer is moved into craNS
// and carries the same name. The returned cleanup removes the pair.
func connectTrunk(t *testing.T, craNS ns.NetNS, trunk string) {
	t.Helper()
	peerTmp := trunk + "p"
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: trunk},
		PeerName:  peerTmp,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("adding trunk veth: %v", err)
	}
	t.Cleanup(func() {
		if link, err := netlink.LinkByName(trunk); err == nil {
			_ = netlink.LinkDel(link)
		}
	})
	peer, err := netlink.LinkByName(peerTmp)
	if err != nil {
		t.Fatalf("looking up trunk peer: %v", err)
	}
	if err := netlink.LinkSetNsFd(peer, int(craNS.Fd())); err != nil {
		t.Fatalf("moving trunk peer into CRA netns: %v", err)
	}
	if err := craNS.Do(func(_ ns.NetNS) error {
		link, e := netlink.LinkByName(peerTmp)
		if e != nil {
			return fmt.Errorf("looking up moved peer: %w", e)
		}
		return netlink.LinkSetName(link, trunk)
	}); err != nil {
		t.Fatalf("renaming trunk peer: %v", err)
	}
}

func TestDiscoverCRANetnsByTrunk(t *testing.T) {
	requireRoot(t)

	// Use a unique trunk name so auto-discovery matches only this netns even if
	// other test namespaces are still present.
	const trunk = "hbndisco0"

	// A decoy namespace that merely owns an interface *named* like the trunk —
	// what a pod could set up through a Multus annotation — must never be
	// mistaken for the CRA netns. It is created first so a name-based scan would
	// hit it before the real one.
	decoyNS := newCRANetns(t, trunk)
	defer testutils.UnmountNS(decoyNS) //nolint:errcheck

	conf := &NetConf{VRF: "cluster", NodeConfig: NodeConfig{CRANetns: "auto", TrunkInterface: trunk}}
	if _, err := resolveCRANetnsPath(conf); err == nil {
		t.Fatalf("resolveCRANetnsPath(auto) must fail without a host-side trunk veth")
	}

	craNS, err := testutils.NewNS()
	if err != nil {
		t.Fatalf("create CRA netns: %v", err)
	}
	defer testutils.UnmountNS(craNS) //nolint:errcheck
	connectTrunk(t, craNS, trunk)

	got, err := resolveCRANetnsPath(conf)
	if err != nil {
		t.Fatalf("resolveCRANetnsPath(auto): %v", err)
	}
	if got != craNS.Path() {
		t.Errorf("resolveCRANetnsPath(auto) = %q, want %q", got, craNS.Path())
	}

	// An explicit path is accepted only for the namespace that owns the trunk
	// peer: the real CRA netns passes, the decoy (and the host netns, which a
	// tenant NAD could name) is refused.
	conf.CRANetns = craNS.Path()
	if got, err = resolveCRANetnsPath(conf); err != nil || got != craNS.Path() {
		t.Errorf("resolveCRANetnsPath(explicit CRA path) = %q, %v; want %q", got, err, craNS.Path())
	}
	for _, bad := range []string{decoyNS.Path(), "/proc/1/ns/net"} {
		conf.CRANetns = bad
		if _, err := resolveCRANetnsPath(conf); err == nil {
			t.Errorf("resolveCRANetnsPath(%q) accepted a netns that does not own the trunk peer", bad)
		}
	}
}
