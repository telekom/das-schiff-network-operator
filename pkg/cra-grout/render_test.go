package cra

import (
	"strings"
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

func TestRenderGrcliRoutedAndEVPN(t *testing.T) {
	base := &config.BaseConfig{
		VTEPLoopbackIP: "10.50.0.10",
		ClusterVRF:     config.BaseVRF{Name: "cluster"},
		ManagementVRF:  config.BaseVRF{Name: "mgmt"},
	}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		ClusterVRF: &v1alpha1.VRF{
			RoutedPorts: []v1alpha1.RoutedPort{
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
		"interface add vxlan l3vni5000 vni 5000 local 10.50.0.10 vrf tenant-a",
		"interface add bridge br2000 vrf tenant-a",
		"address add 10.0.0.1/24 iface br2000",
		"interface add vxlan l2vni2000 vni 2000 local 10.50.0.10 domain br2000",
		"interface add port cra9999 devargs net_tap0,iface=cra9999 domain br2000",
		"interface add port cra0123 devargs net_tap1,iface=cra0123 vrf cluster",
		"address add 169.254.1.1/32 iface cra0123",
		"address add fe80::1/128 iface cra0123",
		"nexthop add l3 iface cra0123 id 1 address 10.201.0.10",
		"route add 10.201.0.10/32 via id 1 vrf cluster",
		"nexthop add l3 iface cra0123 id 2 address fd00:201::10",
		"route add fd00:201::10/128 via id 2 vrf cluster",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("rendered grcli missing %q\n---\n%s", want, out)
		}
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
	if !strings.Contains(out, "interface add bridge br2000 vrf tenant-a") ||
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
					RoutedPorts: []v1alpha1.RoutedPort{
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
			want := "interface add port cravm0 devargs net_vhost0,iface=/var/run/vhost/vm0.sock," + tc.wantClientFlag + " vrf cluster"
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
	if !strings.Contains(out, "interface add port cra9999 devargs net_tap0,iface=cra9999 domain br2000") {
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
