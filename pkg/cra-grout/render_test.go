package cra

import (
	"fmt"
	"strings"
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
)

func TestRenderGrcliRoutedAndEVPN(t *testing.T) {
	base := &config.BaseConfig{
		VTEPLoopbackIP: "10.50.0.10",
		ClusterVRF:     config.BaseVRF{Name: "cluster"},
		ManagementVRF:  config.BaseVRF{Name: "mgmt"},
	}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		ClusterVRF: &v1alpha1.VRF{
			WorkloadPorts: []v1alpha1.WorkloadPort{
				{
					Interface: "cra0123",
					GatewayV4: "169.254.1.1/32",
					GatewayV6: "fe80::1/128",
					HostRoutes: []string{
						"10.201.0.10/32",
						"fd00:201::10/128",
					},
				},
			},
		},
		FabricVRFs: map[string]v1alpha1.FabricVRF{
			"tenant-a": {VRF: v1alpha1.VRF{}, VNI: 5000},
			"mgmt":     {VRF: v1alpha1.VRF{}, VNI: 999},
		},
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {
				VNI:  2000,
				VLAN: 100,
				IRB:  &v1alpha1.IRB{VRF: "tenant-a", MACAddress: "00:11:22:33:44:55", IPAddresses: []string{"10.0.0.1/24"}},
				AttachedPorts: []v1alpha1.AttachedPort{
					{Interface: "cra9999"},
				},
			},
		},
	}

	out, err := RenderGrcli(base, spec)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}

	mustContain := []string{
		"interface add vrf cluster",
		"interface add vrf tenant-a",
		"interface add vxlan l3vni5000 vni 5000 local 10.50.0.10 vrf tenant-a mtu 9000",
		"interface add bridge br2000 vrf tenant-a mtu 9000",
		"address add 10.0.0.1/24 iface br2000",
		"interface add vxlan l2vni2000 vni 2000 local 10.50.0.10 domain br2000 mtu 9000",
		"interface add port cra9999 devargs net_tap_cra9999,iface=cra9999_dp mtu 1500 domain br2000",
		"interface add port cra0123 devargs net_tap_cra0123,iface=cra0123_dp mtu 1500 vrf cluster",
		"address add fe80::1/128 iface cra0123",
		// The ids are derived from the nexthop, so they are computed here
		// rather than hardcoded: pinning the hash would test the hash, while
		// what matters is that the route references the nexthop just added.
		fmt.Sprintf("nexthop add l3 iface cra0123 id %d address fd00:201::10", nexthopID("cra0123", "fd00:201::10")),
		fmt.Sprintf("route add fd00:201::10/128 via id %d vrf cluster", nexthopID("cra0123", "fd00:201::10")),
		// The IPv4 host route resolves over the IPv6 nexthop rather than one of
		// its own. A routed port carries no IPv4 address on this datapath, and
		// grout sources an ARP request from the outgoing interface, so an IPv4
		// nexthop there could never leave state=failed and the workload would be
		// unreachable from the fabric.
		fmt.Sprintf("route add 10.201.0.10/32 via id %d vrf cluster", nexthopID("cra0123", "fd00:201::10")),
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("rendered grcli missing %q\n---\n%s", want, out)
		}
	}

	// The IPv4 gateway must never be programmed on this datapath even though the
	// port asks for one: grout has a single node-global IPv4 address table, so
	// the shared 169.254.1.1/32 fits on exactly one port and the next routed
	// attachment on the node would be rejected with EADDRINUSE. The pod reaches
	// the fabric over IPv4 with the per-interface-scoped fe80::1 as its
	// next-hop instead.
	if strings.Contains(out, "169.254.1.1") {
		t.Errorf("rendered grcli programs the shared IPv4 link-local gateway\n---\n%s", out)
	}

	// For the same reason there must be no IPv4 nexthop on a routed port. grout
	// picks an ARP request's source address from the outgoing interface and
	// drops the request when the interface has no IPv4 address, so such a
	// nexthop stays in state=failed forever and the fabric cannot reach the
	// workload at all -- a silent black hole rather than a rejected line.
	// Matched on the whole line: testing the two halves separately would also
	// accept an IPv4 nexthop that merely sat on a different line.
	if strings.Contains(out, "nexthop add l3 iface cra0123 id "+
		fmt.Sprint(nexthopID("cra0123", "10.201.0.10"))+" address 10.201.0.10") {
		t.Errorf("rendered grcli creates an unresolvable IPv4 nexthop on a routed port\n---\n%s", out)
	}

	// grcli applies a batch line by line, so borrowing an id is only valid if
	// the nexthop was declared earlier in the batch. Presence alone would still
	// pass with the two lines the wrong way round.
	nhLine := fmt.Sprintf("nexthop add l3 iface cra0123 id %d address fd00:201::10",
		nexthopID("cra0123", "fd00:201::10"))
	v4Line := fmt.Sprintf("route add 10.201.0.10/32 via id %d vrf cluster",
		nexthopID("cra0123", "fd00:201::10"))
	if nhIdx, v4Idx := strings.Index(out, nhLine), strings.Index(out, v4Line); nhIdx > v4Idx {
		t.Errorf("IPv4 route borrows nexthop id before it is declared\n---\n%s", out)
	}

	// The management VRF must be skipped (never rendered as a fabric VRF/L3VNI).
	if strings.Contains(out, "vrf mgmt") || strings.Contains(out, "l3vni999") {
		t.Errorf("management VRF must not be rendered:\n%s", out)
	}
}

// TestRenderGrcliL3VNIHasNoAddress guards the EVPN IRB invariant: the anycast
// gateway IP lives on the L2VNI bridge SVI (bound to a VRF), and the L3VNI is
// pure L3 transit with NO SVI address. Regression guard for the user-emphasized
// rule ("why do you want an IP on a L3VNI SVI?").
func TestRenderGrcliL3VNIHasNoAddress(t *testing.T) {
	base := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		FabricVRFs: map[string]v1alpha1.FabricVRF{"tenant-a": {VRF: v1alpha1.VRF{}, VNI: 5000}},
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {
				VNI: 2000, VLAN: 100,
				IRB: &v1alpha1.IRB{VRF: "tenant-a", IPAddresses: []string{"10.0.0.1/24"}},
			},
		},
	}
	out, err := RenderGrcli(base, spec)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}
	if strings.Contains(out, "iface l3vni5000") {
		t.Errorf("L3VNI must carry NO address (pure L3 transit); got:\n%s", out)
	}
	// The IRB address must be on the L2VNI bridge SVI, bound to the tenant VRF.
	if !strings.Contains(out, "interface add bridge br2000 vrf tenant-a mtu 9000") ||
		!strings.Contains(out, "address add 10.0.0.1/24 iface br2000") {
		t.Errorf("IRB address must be on the L2VNI bridge SVI in the tenant VRF; got:\n%s", out)
	}
}

// TestRenderGrcliL2VNIBeforeSVIAddress guards the ordering: the L2VNI VXLAN is
// attached to the bridge domain BEFORE the SVI address is added, so grout has a
// fully-formed L2VNI bridge when the anycast-gateway address is assigned.
func TestRenderGrcliL2VNIBeforeSVIAddress(t *testing.T) {
	base := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		FabricVRFs: map[string]v1alpha1.FabricVRF{"tenant-a": {VRF: v1alpha1.VRF{}, VNI: 5000}},
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {
				VNI: 2000, VLAN: 100,
				IRB: &v1alpha1.IRB{VRF: "tenant-a", IPAddresses: []string{"10.0.0.1/24"}},
			},
		},
	}
	out, err := RenderGrcli(base, spec)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}
	vxlanIdx := strings.Index(out, "interface add vxlan l2vni2000")
	addrIdx := strings.Index(out, "address add 10.0.0.1/24 iface br2000")
	if vxlanIdx < 0 || addrIdx < 0 {
		t.Fatalf("expected both L2VNI vxlan and SVI address lines; got:\n%s", out)
	}
	if vxlanIdx > addrIdx {
		t.Errorf("L2VNI vxlan must be emitted before the SVI address; got:\n%s", out)
	}
}

func TestRenderGrcliVhostUser(t *testing.T) {
	base := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}

	// The two ends of a vhost-user socket take opposite roles: grout inverts the
	// workload-perspective socket mode (like VSR). Workload "client" => grout
	// owns the socket (server, client=0); workload "server" => grout connects
	// (client=1).
	cases := []struct {
		name           string
		workloadMode   string
		wantClientFlag string
	}{
		{"workload client => grout server", "client", "client=0"},
		{"workload server => grout client", "server", "client=1"},
		{"workload empty => grout server", "", "client=0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &v1alpha1.NodeNetworkConfigSpec{
				ClusterVRF: &v1alpha1.VRF{
					WorkloadPorts: []v1alpha1.WorkloadPort{
						{
							Interface:  "cravm0",
							PortWiring: v1alpha1.PortWiring{Transport: v1alpha1.PortTransportVhostUser, SocketPath: "/var/run/vhost/vm0.sock", SocketMode: tc.workloadMode},
							HostRoutes: []string{"10.201.0.20/32"},
						},
					},
				},
			}
			out, err := RenderGrcli(base, spec)
			if err != nil {
				t.Fatalf("RenderGrcli: %v", err)
			}
			want := "interface add port cravm0 devargs net_vhost_cravm0,iface=/var/run/vhost/vm0.sock," + tc.wantClientFlag + " mtu 1500 vrf cluster"
			if !strings.Contains(out, want) {
				t.Errorf("expected net_vhost port %q, got:\n%s", want, out)
			}
		})
	}
}

// TestRenderGrcliTrunkVlanToBridge guards the macvlan-on-trunk L2 datapath: when
// the node has a fabric trunk configured (BaseConfig.TrunkInterfaceName) and a
// Layer2 carries a VLAN, the renderer maps that VLAN on the shared trunk into
// the L2VNI bridge domain via a grout VLAN sub-interface. This lets workloads
// attached with macvlan on the host-side trunk netdev reach the L2VNI, in
// parallel with routed-CNI access ports on the same bridge.
func TestRenderGrcliTrunkVlanToBridge(t *testing.T) {
	base := &config.BaseConfig{
		VTEPLoopbackIP:     "10.50.0.10",
		TrunkInterfaceName: "hbn",
		ClusterVRF:         config.BaseVRF{Name: "cluster"},
	}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		FabricVRFs: map[string]v1alpha1.FabricVRF{"tenant-a": {VRF: v1alpha1.VRF{}, VNI: 5000}},
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {
				VNI: 2000, VLAN: 501,
				IRB: &v1alpha1.IRB{VRF: "tenant-a", IPAddresses: []string{"10.0.0.1/24"}},
				AttachedPorts: []v1alpha1.AttachedPort{
					{Interface: "cra9999"},
				},
			},
		},
	}
	out, err := RenderGrcli(base, spec)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}

	// The trunk VLAN sub-interface must map VLAN 501 into the L2VNI bridge.
	wantTrunk := "interface add vlan hbn.501 parent hbn vlan_id 501 domain br2000"
	if !strings.Contains(out, wantTrunk) {
		t.Errorf("expected trunk VLAN mapping %q, got:\n%s", wantTrunk, out)
	}
	// The routed-CNI access port stays an independent member of the same bridge.
	if !strings.Contains(out, "interface add port cra9999 devargs net_tap_cra9999,iface=cra9999_dp mtu 1500 domain br2000") {
		t.Errorf("expected access tap port on br2000; got:\n%s", out)
	}
	// The trunk VLAN sub-interface must be enslaved AFTER the bridge exists.
	brIdx := strings.Index(out, "interface add bridge br2000")
	vlanIdx := strings.Index(out, wantTrunk)
	if brIdx < 0 || vlanIdx < 0 || brIdx > vlanIdx {
		t.Errorf("trunk VLAN sub-interface must come after the bridge is created; got:\n%s", out)
	}
}

// TestRenderGrcliNoTrunkNoVlanMapping guards the negative cases: no trunk VLAN
// sub-interface is emitted when the node has no trunk configured, nor when the
// Layer2 carries no VLAN.
func TestRenderGrcliNoTrunkNoVlanMapping(t *testing.T) {
	// (a) No trunk configured => no VLAN sub-interface even though the L2 has a VLAN.
	noTrunk := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {VNI: 2000, VLAN: 501},
		},
	}
	out, err := RenderGrcli(noTrunk, spec)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}
	if strings.Contains(out, "interface add vlan") {
		t.Errorf("no trunk configured: must not emit a VLAN sub-interface; got:\n%s", out)
	}

	// (b) Trunk configured but L2 has no VLAN => no VLAN sub-interface.
	withTrunk := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", TrunkInterfaceName: "hbn", ClusterVRF: config.BaseVRF{Name: "cluster"}}
	specNoVlan := &v1alpha1.NodeNetworkConfigSpec{
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {VNI: 2000},
		},
	}
	out, err = RenderGrcli(withTrunk, specNoVlan)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}
	if strings.Contains(out, "interface add vlan") {
		t.Errorf("L2 without VLAN: must not emit a VLAN sub-interface; got:\n%s", out)
	}
}

// TestRenderGrcliDevargsAreStableAcrossPortRemoval pins the property that makes
// the desired-state replay idempotent: a port's DPDK vdev name must depend only
// on the port, never on how many ports happen to precede it.
//
// The failure this guards against is not cosmetic. With positional naming,
// removing the first of two ports renumbers the survivor; grout tolerates the
// re-add as EEXIST so nothing looks wrong, but the next NEW port is rendered
// with an index the survivor's live vdev still holds, and DPDK rejects the
// duplicate name with EEXIST for good.
func TestRenderGrcliDevargsAreStableAcrossPortRemoval(t *testing.T) {
	base := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}

	render := func(ports ...v1alpha1.WorkloadPort) string {
		out, err := RenderGrcli(base, &v1alpha1.NodeNetworkConfigSpec{
			ClusterVRF: &v1alpha1.VRF{WorkloadPorts: ports},
		})
		if err != nil {
			t.Fatalf("RenderGrcli: %v", err)
		}
		return out
	}

	portA := v1alpha1.WorkloadPort{Interface: "craaaa"}
	portB := v1alpha1.WorkloadPort{Interface: "crabbb"}
	portC := v1alpha1.WorkloadPort{Interface: "craccc"}

	wantB := "interface add port crabbb devargs net_tap_crabbb,iface=crabbb_dp mtu 1500 vrf cluster"

	// B is second here and first after A is removed; its devargs must not move.
	if out := render(portA, portB); !strings.Contains(out, wantB) {
		t.Errorf("with A present, expected %q, got:\n%s", wantB, out)
	}
	if out := render(portB); !strings.Contains(out, wantB) {
		t.Errorf("after A was removed, expected %q, got:\n%s", wantB, out)
	}

	// And a newly added port must not collide with the survivor's vdev name.
	out := render(portB, portC)
	if !strings.Contains(out, wantB) {
		t.Errorf("expected %q, got:\n%s", wantB, out)
	}
	if !strings.Contains(out, "net_tap_craccc,iface=craccc_dp") {
		t.Errorf("expected C to get its own vdev name, got:\n%s", out)
	}
	if strings.Count(out, "net_tap_crabbb") != 1 {
		t.Errorf("vdev name reused across ports:\n%s", out)
	}
}

// TestNexthopIDsAreStableAndDistinct guards the nexthop half of the same
// idempotency property as the vdev names.
//
// The bug this prevents is the quiet one: with counter-allocated ids, removing a
// port shifts every later nexthop's id down. grout tolerates the re-add of an
// existing id as EEXIST -- keeping the OLD address and interface -- and the
// following `route add ... via id N` then binds a pod's host route to a
// different pod's interface. Nothing errors; the traffic just goes to the wrong
// place.
func TestNexthopIDsAreStableAndDistinct(t *testing.T) {
	// Same nexthop, same id, no matter what else is configured.
	if a, b := nexthopID("cra0123", "10.0.0.1"), nexthopID("cra0123", "10.0.0.1"); a != b {
		t.Errorf("nexthopID is not deterministic: %d != %d", a, b)
	}

	// Different nexthops must not share an id: that is what would let one
	// route resolve through another port.
	seen := map[uint32]string{}
	for _, nh := range []struct{ iface, addr string }{
		{"cra0123", "10.0.0.1"},
		{"cra0123", "10.0.0.2"},
		{"cra0123", "fd00::1"},
		{"cra9999", "10.0.0.1"},
		{"crabbbb", "10.0.0.1"},
	} {
		id := nexthopID(nh.iface, nh.addr)
		key := nh.iface + "/" + nh.addr
		if prev, dup := seen[id]; dup {
			t.Errorf("nexthop id %d collides between %s and %s", id, prev, key)
		}
		seen[id] = key
		if id == 0 {
			t.Errorf("%s got nexthop id 0, which grout treats as unset", key)
		}
	}

	// The id must not be affected by the removal of an unrelated port, which is
	// exactly what a counter would not survive.
	base := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}
	render := func(ports ...v1alpha1.WorkloadPort) string {
		out, err := RenderGrcli(base, &v1alpha1.NodeNetworkConfigSpec{
			ClusterVRF: &v1alpha1.VRF{WorkloadPorts: ports},
		})
		if err != nil {
			t.Fatalf("RenderGrcli: %v", err)
		}
		return out
	}
	portA := v1alpha1.WorkloadPort{Interface: "craaaa", HostRoutes: []string{"10.0.0.1/32"}}
	portB := v1alpha1.WorkloadPort{Interface: "crabbb", HostRoutes: []string{"10.0.0.2/32"}}

	wantB := fmt.Sprintf("route add 10.0.0.2/32 via id %d vrf cluster", nexthopID("crabbb", "10.0.0.2"))
	if out := render(portA, portB); !strings.Contains(out, wantB) {
		t.Errorf("with A present, expected %q, got:\n%s", wantB, out)
	}
	if out := render(portB); !strings.Contains(out, wantB) {
		t.Errorf("after A was removed, B's nexthop id moved; expected %q, got:\n%s", wantB, out)
	}
}

// A batch line is split on whitespace and executed as a grcli command, so a
// socket path carrying a separator would stop being an argument and start
// being one. The value is validated where it enters the cluster; the renderer
// refuses it as well, because it is the component that would run it.
func TestRenderGrcliRejectsUnsafeVhostUserSocketPaths(t *testing.T) {
	base := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}

	for _, path := range []string{
		"",
		"/run/vhost/socket interface del craaaa",
		"/run/vhost/socket\ninterface del craaaa",
		"/run/vhost/socket,client=0",
	} {
		_, err := RenderGrcli(base, &v1alpha1.NodeNetworkConfigSpec{
			ClusterVRF: &v1alpha1.VRF{WorkloadPorts: []v1alpha1.WorkloadPort{{
				Interface:  "craaaa",
				PortWiring: v1alpha1.PortWiring{Transport: v1alpha1.PortTransportVhostUser, SocketPath: path},
			}}},
		})
		if err == nil {
			t.Errorf("RenderGrcli with socket path %q = nil error, want a rejection", path)
		}
	}
}

// TestRenderGrcliWorkloadTrunkPort covers a workload-CNI trunk: one port
// carrying two tagged members into two different L2VNI bridges.
//
// The three properties asserted here are the ones a trunk stops working
// without. The port is created exactly once, even though it appears once per
// domain in AttachedPorts -- grout binds a DPDK vdev by name, so a second
// creation is a permanent EEXIST rather than a no-op. It is created with no
// binding, which leaves it in grout's VRF mode: iface_input only looks a frame's
// VLAN id up against an interface's sub-interfaces in that mode, so a port bound
// to a domain instead would swallow every frame into that one domain and the
// members below would never receive anything. And the port precedes the
// sub-interfaces that name it as a parent, because a batch line is applied the
// moment it is read.
func TestRenderGrcliWorkloadTrunkPort(t *testing.T) {
	base := &config.BaseConfig{
		VTEPLoopbackIP: "10.50.0.10",
		ClusterVRF:     config.BaseVRF{Name: "cluster"},
	}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		FabricVRFs: map[string]v1alpha1.FabricVRF{"tenant-a": {VRF: v1alpha1.VRF{}, VNI: 5000}},
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {
				VNI: 2000, VLAN: 100, MTU: 9000,
				AttachedPorts: []v1alpha1.AttachedPort{
					// Inherited id: the workload tags with the domain's own VLAN.
					{Interface: "cra0777", VLAN: 100, MTU: 9000},
				},
			},
			"green": {
				VNI: 3000, VLAN: 200, MTU: 9000,
				AttachedPorts: []v1alpha1.AttachedPort{
					// Translated id: the workload tags 3000, the fabric uses 200.
					{Interface: "cra0777", VLAN: 3000, MTU: 9000},
				},
			},
		},
	}

	out, err := RenderGrcli(base, spec)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}

	wantPort := "interface add port cra0777 devargs net_tap_cra0777,iface=cra0777_dp mtu 9000\n"
	if n := strings.Count(out, wantPort); n != 1 {
		t.Errorf("trunk port rendered %d times, want exactly 1:\n%s", n, out)
	}
	// No `domain`/`vrf` binding on the port itself, or grout skips VLAN demux.
	if strings.Contains(out, "iface=cra0777_dp mtu 9000 domain") {
		t.Errorf("trunk port must not be bound to a bridge domain:\n%s", out)
	}

	for _, want := range []string{
		"interface add vlan cra0777.100 parent cra0777 vlan_id 100 mtu 9000 domain br2000",
		"interface add vlan cra0777.3000 parent cra0777 vlan_id 3000 mtu 9000 domain br3000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing trunk member %q, got:\n%s", want, out)
		}
		if strings.Index(out, wantPort) > strings.Index(out, want) {
			t.Errorf("trunk member %q rendered before its parent port:\n%s", want, out)
		}
	}
}

// TestRenderGrcliTrunkMemberDefaultsMTU covers a member that requested no MTU:
// it is sized with the same default the CNI applied to the workload's own
// netdev, rather than left at whatever the DPDK device happens to default to.
func TestRenderGrcliTrunkMemberDefaultsMTU(t *testing.T) {
	base := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {
				VNI: 2000, VLAN: 100,
				AttachedPorts: []v1alpha1.AttachedPort{{Interface: "cra0777", VLAN: 100}},
			},
		},
	}

	out, err := RenderGrcli(base, spec)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}
	for _, want := range []string{
		fmt.Sprintf("interface add port cra0777 devargs net_tap_cra0777,iface=cra0777_dp mtu %d", workloadcni.DefaultPortMTU),
		fmt.Sprintf("interface add vlan cra0777.100 parent cra0777 vlan_id 100 mtu %d domain br2000", workloadcni.DefaultPortMTU),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q, got:\n%s", want, out)
		}
	}
}

// TestRenderGrcliVhostUserTrunkPort covers a VM attached at layer 2 over a
// vhost-user socket and trunking several domains: the same VLAN demux applies,
// so the net_vhost port is created unbound and the members hang off it.
func TestRenderGrcliVhostUserTrunkPort(t *testing.T) {
	base := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {
				VNI: 2000, VLAN: 100,
				AttachedPorts: []v1alpha1.AttachedPort{{
					Interface:  "v0abcde",
					VLAN:       100,
					MTU:        1500,
					PortWiring: v1alpha1.PortWiring{Transport: v1alpha1.PortTransportVhostUser, SocketPath: "/run/vhost/vm0.sock", SocketMode: v1alpha1.SocketModeServer},
				}},
			},
		},
	}

	out, err := RenderGrcli(base, spec)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}
	for _, want := range []string{
		// SocketMode is the WORKLOAD's side, so a workload-side server makes
		// grout the client.
		"interface add port v0abcde devargs net_vhost_v0abcde,iface=/run/vhost/vm0.sock,client=1 mtu 1500\n",
		"interface add vlan v0abcde.100 parent v0abcde vlan_id 100 mtu 1500 domain br2000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q, got:\n%s", want, out)
		}
	}
}

// TestRenderGrcliRejectsInjectableTrunkSocketPath covers the trunk path going
// through the same gate as the routed one: a batch line is split on whitespace
// and run as root, so a socket path carrying a separator would stop being an
// argument and start being another grcli command.
func TestRenderGrcliRejectsInjectableTrunkSocketPath(t *testing.T) {
	base := &config.BaseConfig{VTEPLoopbackIP: "10.50.0.10", ClusterVRF: config.BaseVRF{Name: "cluster"}}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {
				VNI: 2000, VLAN: 100,
				AttachedPorts: []v1alpha1.AttachedPort{{
					Interface:  "v0abcde",
					VLAN:       100,
					PortWiring: v1alpha1.PortWiring{Transport: v1alpha1.PortTransportVhostUser, SocketPath: "/run/vhost/vm0.sock interface del hbn"},
				}},
			},
		},
	}

	if _, err := RenderGrcli(base, spec); err == nil {
		t.Error("RenderGrcli accepted a socket path carrying a command separator")
	}
}

// TestRenderGrcliClampsTrunkMemberMTUToItsDomain covers the heterogeneous-MTU
// trunk: a trunk is admitted when only one member's Layer2 carries the
// requested MTU, so the smaller domains on the same port see a request larger
// than they can carry. The sub-interface has to follow its own domain, or the
// batch names an MTU and a bridge that disagree on one line.
func TestRenderGrcliClampsTrunkMemberMTUToItsDomain(t *testing.T) {
	spec := &v1alpha1.NodeNetworkConfigSpec{
		Layer2s: map[string]v1alpha1.Layer2{
			"big": {
				VNI: 2001, VLAN: 501, MTU: 9000,
				AttachedPorts: []v1alpha1.AttachedPort{
					{Interface: "cra0123", VLAN: 501, MTU: 9000},
				},
			},
			"small": {
				VNI: 2002, VLAN: 502, MTU: 1500,
				AttachedPorts: []v1alpha1.AttachedPort{
					{Interface: "cra0123", VLAN: 502, MTU: 9000},
				},
			},
		},
	}
	base := &config.BaseConfig{
		VTEPLoopbackIP: "10.50.0.10",
		ClusterVRF:     config.BaseVRF{Name: "cluster"},
	}
	out, err := RenderGrcli(base, spec)
	if err != nil {
		t.Fatalf("RenderGrcli: %v", err)
	}

	// The parent carries the largest member, so the big domain still fits.
	wantParent := "interface add port cra0123 devargs net_tap_cra0123,iface=cra0123_dp mtu 9000"
	if !strings.Contains(out, wantParent) {
		t.Errorf("trunk parent not sized for its largest member: want %q\n---\n%s", wantParent, out)
	}
	for _, want := range []string{
		"interface add vlan cra0123.501 parent cra0123 vlan_id 501 mtu 9000 domain br2001",
		"interface add vlan cra0123.502 parent cra0123 vlan_id 502 mtu 1500 domain br2002",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered grcli missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderWorkloadHostRoutesFamilies covers the host-route family
// combinations a routed port can present. Only the dual-stack case can borrow
// an IPv6 nexthop for its IPv4 route; the others must still render a valid
// batch so the rest of the node's configuration keeps applying.
func TestRenderWorkloadHostRoutesFamilies(t *testing.T) {
	v6ID := func(addr string) uint32 { return nexthopID("cra0123", addr) }

	tests := []struct {
		name       string
		hostRoutes []string
		want       []string
		notWant    []string
	}{
		{
			name:       "ipv6 only",
			hostRoutes: []string{"fd00:201::10/128"},
			want: []string{
				fmt.Sprintf("nexthop add l3 iface cra0123 id %d address fd00:201::10", v6ID("fd00:201::10")),
				fmt.Sprintf("route add fd00:201::10/128 via id %d vrf cluster", v6ID("fd00:201::10")),
			},
		},
		{
			// Nothing to borrow, so the port keeps its own IPv4 nexthop. It
			// cannot resolve on this datapath, but the batch stays valid.
			name:       "ipv4 only",
			hostRoutes: []string{"10.201.0.10/32"},
			want: []string{
				fmt.Sprintf("nexthop add l3 iface cra0123 id %d address 10.201.0.10", v6ID("10.201.0.10")),
				fmt.Sprintf("route add 10.201.0.10/32 via id %d vrf cluster", v6ID("10.201.0.10")),
			},
		},
		{
			// Every IPv4 route borrows the *first* IPv6 nexthop, so the render
			// stays byte-identical for a given spec.
			name: "multiple ipv6 routes",
			hostRoutes: []string{
				"10.201.0.10/32",
				"fd00:201::10/128",
				"fd00:201::11/128",
			},
			want: []string{
				fmt.Sprintf("route add fd00:201::11/128 via id %d vrf cluster", v6ID("fd00:201::11")),
				fmt.Sprintf("route add 10.201.0.10/32 via id %d vrf cluster", v6ID("fd00:201::10")),
			},
			notWant: []string{
				fmt.Sprintf("route add 10.201.0.10/32 via id %d vrf cluster", v6ID("fd00:201::11")),
				"address 10.201.0.10",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Batch{}
			port := &v1alpha1.WorkloadPort{Interface: "cra0123", HostRoutes: tt.hostRoutes}
			if err := renderWorkloadHostRoutes(b, port, "cluster"); err != nil {
				t.Fatalf("renderWorkloadHostRoutes: %v", err)
			}
			out := b.String()
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q\n---\n%s", want, out)
				}
			}
			for _, bad := range tt.notWant {
				if strings.Contains(out, bad) {
					t.Errorf("unexpected %q\n---\n%s", bad, out)
				}
			}
		})
	}
}
