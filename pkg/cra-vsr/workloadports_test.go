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

package cra

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

// craPortIfName is the reusable test interface name for the moved CRA-side port.
const craPortIfName = "craport0"

// TestApplyWorkloadPortsXML verifies that layering a workload port onto an existing
// VRF (as the VSR reconcile path does) renders the NETCONF constructs VSR
// expects. The VRF is composed first (mirroring LookupVRF) and then merged into.
func TestApplyWorkloadPortsXML(t *testing.T) {
	vrf := &VRF{
		Name:       "cluster",
		Interfaces: &Interfaces{},
		Routing:    &Routing{NCOperation: Merge, Static: &StaticRouting{}},
	}
	if _, err := applyWorkloadPorts(vrf, WorkloadPort{
		IfName:     craPortIfName,
		GatewayV4:  "169.254.100.100/32",
		GatewayV6:  "fd00:7:caa5:1::/128",
		HostRoutes: []string{"10.0.0.5/32", "fd00:200::5/128"},
	}); err != nil {
		t.Fatalf("applyWorkloadPorts: %v", err)
	}

	out, err := xml.Marshal(vrf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	// Assert the key NETCONF constructs are rendered as VSR expects.
	wants := []string{
		"<name>cluster</name>",
		`<infrastructure xmlns="urn:6wind:vrouter/infrastructure"><name>craport0</name>`,
		"<port>infra-craport0</port>",
		"<ipv4><address><ip>169.254.100.100/32</ip></address></ipv4>",
		"<ipv6><address><ip>fd00:7:caa5:1::/128</ip></address></ipv6>",
		"<ipv4-route><destination>10.0.0.5/32</destination><next-hop><next-hop>craport0</next-hop></next-hop></ipv4-route>",
		"<ipv6-route><destination>fd00:200::5/128</destination><next-hop><next-hop>craport0</next-hop></next-hop></ipv6-route>",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("rendered XML missing %q\nfull XML:\n%s", w, got)
		}
	}
}

func TestApplyWorkloadPortsRetainsLongInfrastructurePortReference(t *testing.T) {
	const ifName = "cra012345678901"
	vrf := &VRF{
		Name:       "cluster",
		Interfaces: &Interfaces{},
		Routing:    &Routing{NCOperation: Merge, Static: &StaticRouting{}},
	}

	if _, err := applyWorkloadPorts(vrf, WorkloadPort{IfName: ifName}); err != nil {
		t.Fatalf("applyWorkloadPorts: %v", err)
	}

	if len(vrf.Interfaces.Infras) != 1 {
		t.Fatalf("expected one infrastructure interface, got %+v", vrf.Interfaces.Infras)
	}
	infra := vrf.Interfaces.Infras[0]
	if infra.Name != ifName {
		t.Errorf("infrastructure interface name = %q, want %q", infra.Name, ifName)
	}
	if infra.Port == nil || *infra.Port != infraPortPrefix+ifName {
		t.Errorf("infrastructure port = %v, want %q", infra.Port, infraPortPrefix+ifName)
	}
	if infra.Port != nil && len(*infra.Port) <= 15 {
		t.Errorf("infrastructure port reference %q unexpectedly fits the interface-name limit", *infra.Port)
	}
}

func TestApplyWorkloadPortsErrors(t *testing.T) {
	vrf := &VRF{Name: "cluster"}
	if _, err := applyWorkloadPorts(vrf, WorkloadPort{}); err == nil {
		t.Errorf("expected error for empty ifname")
	}
	if _, err := applyWorkloadPorts(vrf, WorkloadPort{
		IfName:    craPortIfName,
		Transport: v1alpha1.PortTransportVhostUser,
	}); err == nil {
		t.Errorf("expected error for vhostuser transport without a socket path")
		t.Errorf("expected error for empty ifname")
	}
}

// TestApplyWorkloadPortsVhostUserXML verifies a vhostuser workload port renders as a
// fpvhost interface (port fpvhost-<ifname>) instead of an infrastructure port,
// and returns a fast-path fpvhost virtual-port with the inverted socket mode.
func TestApplyWorkloadPortsVhostUserXML(t *testing.T) {
	vrf := &VRF{
		Name:       "cluster",
		Interfaces: &Interfaces{},
		Routing:    &Routing{NCOperation: Merge, Static: &StaticRouting{}},
	}
	vports, err := applyWorkloadPorts(vrf, WorkloadPort{
		IfName:     craPortIfName,
		Transport:  v1alpha1.PortTransportVhostUser,
		SocketPath: "/run/vsr-vhost-user/3f9a2b1c7d/socket",
		SocketMode: "server", // workload server -> VSR client
		GatewayV4:  "169.254.100.100/32",
		HostRoutes: []string{"10.0.0.5/32"},
	})
	if err != nil {
		t.Fatalf("applyWorkloadPorts: %v", err)
	}

	if len(vrf.Interfaces.Infras) != 0 {
		t.Errorf("vhostuser port must not render an infrastructure interface: %+v", vrf.Interfaces.Infras)
	}
	if len(vports) != 1 || vports[0].Name != "fpvhost-"+craPortIfName {
		t.Fatalf("expected one fpvhost virtual-port fpvhost-%s, got %+v", craPortIfName, vports)
	}
	if vports[0].SocketMode == nil || *vports[0].SocketMode != v1alpha1.SocketModeClient {
		t.Errorf("workload socket-mode server must invert to VSR client, got %v", vports[0].SocketMode)
	}
	if vports[0].SocketPath == nil || *vports[0].SocketPath != "/run/vsr-vhost-user/3f9a2b1c7d/socket" {
		t.Errorf("fpvhost virtual-port must carry the host-side socket path, got %v", vports[0].SocketPath)
	}

	out, err := xml.Marshal(vrf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, w := range []string{
		"<fpvhost><name>craport0</name>",
		"<port>fpvhost-craport0</port>",
		"<ipv4><address><ip>169.254.100.100/32</ip></address></ipv4>",
		"<ipv4-route><destination>10.0.0.5/32</destination><next-hop><next-hop>craport0</next-hop></next-hop></ipv4-route>",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("rendered XML missing %q\nfull XML:\n%s", w, got)
		}
	}
}

func TestApplyWorkloadPortsVhostUserRetainsLongVirtualPortReference(t *testing.T) {
	const ifName = "vho012345678901"
	vrf := &VRF{
		Name:       "cluster",
		Interfaces: &Interfaces{},
		Routing:    &Routing{NCOperation: Merge, Static: &StaticRouting{}},
	}

	vports, err := applyWorkloadPorts(vrf, WorkloadPort{
		IfName:     ifName,
		Transport:  v1alpha1.PortTransportVhostUser,
		SocketPath: "/run/vsr-vhost-user/3f9a2b1c7d/socket",
		SocketMode: v1alpha1.SocketModeServer,
	})
	if err != nil {
		t.Fatalf("applyWorkloadPorts: %v", err)
	}

	if len(vrf.Interfaces.Fpvhosts) != 1 {
		t.Fatalf("expected one fpvhost interface, got %+v", vrf.Interfaces.Fpvhosts)
	}
	fpvhost := vrf.Interfaces.Fpvhosts[0]
	wantPort := fpvhostPortPrefix + ifName
	if fpvhost.Name != ifName || fpvhost.Port == nil || *fpvhost.Port != wantPort {
		t.Errorf("fpvhost interface = %+v, want name %q and port %q", fpvhost, ifName, wantPort)
	}
	if len(vports) != 1 || vports[0].Name != wantPort {
		t.Errorf("fpvhost virtual-port = %+v, want name %q", vports, wantPort)
	}
	if len(wantPort) <= 15 {
		t.Errorf("fpvhost port reference %q unexpectedly fits the interface-name limit", wantPort)
	}

	vrouter := &VRouter{}
	registerFpvhostVirtualPorts(vrouter, vports)
	if vrouter.System == nil || vrouter.System.FastPath == nil || vrouter.System.FastPath.VirtualPort == nil ||
		len(vrouter.System.FastPath.VirtualPort.Fpvhosts) != 1 ||
		vrouter.System.FastPath.VirtualPort.Fpvhosts[0].Name != wantPort {
		t.Errorf("registered fpvhost virtual-port = %+v, want %q", vrouter.System, wantPort)
	}
}

// TestRegisterFpvhostVirtualPortsXML verifies the global system fast-path
// virtual-port subtree renders as VSR expects and de-duplicates by name.
func TestRegisterFpvhostVirtualPortsXML(t *testing.T) {
	vrouter := &VRouter{}
	registerFpvhostVirtualPorts(vrouter, []FpvhostVirtualPort{
		newFpvhostVirtualPort("craport0", "/run/vsr-vhost-user/3f9a2b1c7d/socket", "client"), // -> VSR server
	})
	// Duplicate registration must not add a second entry.
	registerFpvhostVirtualPorts(vrouter, []FpvhostVirtualPort{
		newFpvhostVirtualPort("craport0", "/run/vsr-vhost-user/3f9a2b1c7d/socket", "client"),
	})

	if vrouter.System == nil || vrouter.System.FastPath == nil || vrouter.System.FastPath.VirtualPort == nil {
		t.Fatal("expected system fast-path virtual-port subtree to be created")
	}
	if n := len(vrouter.System.FastPath.VirtualPort.Fpvhosts); n != 1 {
		t.Fatalf("expected 1 de-duplicated fpvhost virtual-port, got %d", n)
	}

	out, err := xml.Marshal(&VRouterConfig{VRouter: *vrouter})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, w := range []string{
		"<system xmlns=\"urn:6wind:vrouter/system\">",
		"<fast-path xmlns=\"urn:6wind:vrouter/fast-path\">",
		"<virtual-port><fpvhost><name>fpvhost-craport0</name>",
		"<sockpath>/run/vsr-vhost-user/3f9a2b1c7d/socket</sockpath>",
		"<sockmode>server</sockmode>",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("rendered XML missing %q\nfull XML:\n%s", w, got)
		}
	}
}

// TestLayer2AttachPortsXML verifies L2-attached ports render as bridge
// link-interfaces (slaves) with a matching interface entry and no L3 addressing.
func TestLayer2AttachPortsXML(t *testing.T) {
	l := &Layer2{vrouter: &VRouter{}}
	intfs := &Interfaces{}
	br := &Bridge{Name: "l2.100"}
	info := &InfoL2{
		vni: 100,
		attachedPorts: []v1alpha1.AttachedPort{
			{Interface: "cra-veth", PortWiring: v1alpha1.PortWiring{Transport: v1alpha1.PortTransportVeth}},
			{Interface: "cra-vho", PortWiring: v1alpha1.PortWiring{
				Transport:  v1alpha1.PortTransportVhostUser,
				SocketPath: "/run/vsr-vhost-user/3f9a2b1c7d/socket",
				SocketMode: "server",
			}},
		},
	}

	if err := l.attachPorts(info, br, intfs); err != nil {
		t.Fatalf("attachPorts: %v", err)
	}

	if len(br.Slaves) != 2 || br.Slaves[0].Name != "cra-veth" || br.Slaves[1].Name != "cra-vho" {
		t.Fatalf("expected both ports enslaved to the bridge, got %+v", br.Slaves)
	}
	if len(intfs.Infras) != 1 || intfs.Infras[0].Name != "cra-veth" {
		t.Errorf("expected a veth infrastructure interface, got %+v", intfs.Infras)
	}
	if len(intfs.Fpvhosts) != 1 || intfs.Fpvhosts[0].Name != "cra-vho" {
		t.Errorf("expected a fpvhost interface, got %+v", intfs.Fpvhosts)
	}
	if intfs.Infras[0].IPv4 != nil || intfs.Infras[0].IPv6 != nil {
		t.Errorf("L2-attached port must carry no L3 addressing")
	}
	// The vhostuser attach must register a global fpvhost virtual-port.
	if l.vrouter.System == nil || len(l.vrouter.System.FastPath.VirtualPort.Fpvhosts) != 1 {
		t.Errorf("expected the vhostuser attach to register a fpvhost virtual-port")
	}

	out, err := xml.Marshal(br)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, w := range []string{
		"<link-interface><slave>cra-veth</slave></link-interface>",
		"<link-interface><slave>cra-vho</slave></link-interface>",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("rendered bridge XML missing %q\nfull XML:\n%s", w, got)
		}
	}
}

// TestApplyWorkloadPortsMerge verifies that workload ports layer onto an
// already-composed VRF (the NNC reconcile path) instead of replacing it: the
// pre-existing infra interface and static routes must be preserved.
func TestApplyWorkloadPortsMerge(t *testing.T) {
	vrf := &VRF{
		Name:       "cluster",
		Interfaces: &Interfaces{Infras: []Infrastructure{{Name: "existing"}}},
		Routing: &Routing{
			Static: &StaticRouting{
				IPv4: []StaticRoute{{Destination: "10.9.9.0/24", NextHops: []NextHop{{NextHop: "blackhole"}}}},
			},
		},
	}

	_, err := applyWorkloadPorts(vrf, WorkloadPort{
		IfName:     craPortIfName,
		GatewayV4:  "169.254.1.1/32",
		HostRoutes: []string{"10.0.0.5/32", "fd00:200::5/128"},
	})
	if err != nil {
		t.Fatalf("applyWorkloadPorts: %v", err)
	}

	if len(vrf.Interfaces.Infras) != 2 || vrf.Interfaces.Infras[0].Name != "existing" {
		t.Errorf("existing infra not preserved: %+v", vrf.Interfaces.Infras)
	}
	if vrf.Interfaces.Infras[1].Port == nil || *vrf.Interfaces.Infras[1].Port != "infra-craport0" {
		t.Errorf("routed infra port = %v", vrf.Interfaces.Infras[1].Port)
	}
	if len(vrf.Routing.Static.IPv4) != 2 || vrf.Routing.Static.IPv4[0].Destination != "10.9.9.0/24" {
		t.Errorf("existing v4 static not preserved: %+v", vrf.Routing.Static.IPv4)
	}
	if vrf.Routing.Static.IPv4[1].NextHops[0].NextHop != craPortIfName {
		t.Errorf("routed v4 next-hop = %q", vrf.Routing.Static.IPv4[1].NextHops[0].NextHop)
	}
	if len(vrf.Routing.Static.IPv6) != 1 || vrf.Routing.Static.IPv6[0].NextHops[0].NextHop != craPortIfName {
		t.Errorf("routed v6 static = %+v", vrf.Routing.Static.IPv6)
	}

	// Empty port list is a no-op.
	before := len(vrf.Interfaces.Infras)
	if _, err := applyWorkloadPorts(vrf); err != nil {
		t.Fatalf("applyWorkloadPorts(empty): %v", err)
	}
	if len(vrf.Interfaces.Infras) != before {
		t.Errorf("empty applyWorkloadPorts mutated vrf")
	}
}

// TestApplyGlobalWorkloadPorts verifies that null-VRF workload ports are rendered
// into the namespace's default (no-l3vrf) table: infra interface + interface
// static routes at namespace level, plus a BGP network statement per host route
// (advertised by the underlay session instead of L3VRF redistribution).
func TestApplyGlobalWorkloadPorts(t *testing.T) {
	ns := &Namespace{
		Interfaces: &Interfaces{},
		Routing: &Routing{
			Static: &StaticRouting{},
			BGP: &BGP{
				AF: &BGPAddrFamily{
					UcastV4: &BGPUcast{
						Network: []BGPUcastNetwork{{Prefix: "10.255.0.1/32"}}, // pre-existing VTEP
					},
				},
			},
		},
	}

	if _, err := applyGlobalWorkloadPorts(ns, WorkloadPort{
		IfName:     craPortIfName,
		GatewayV4:  "169.254.1.1/32",
		GatewayV6:  "fe80::1/128",
		HostRoutes: []string{"10.0.0.5/32", "fd00:200::5/128"},
	}); err != nil {
		t.Fatalf("applyGlobalWorkloadPorts: %v", err)
	}

	// Infra + static routes land at namespace level.
	if len(ns.Interfaces.Infras) != 1 || ns.Interfaces.Infras[0].Port == nil ||
		*ns.Interfaces.Infras[0].Port != "infra-craport0" {
		t.Errorf("namespace infra not rendered: %+v", ns.Interfaces.Infras)
	}
	if len(ns.Routing.Static.IPv4) != 1 || len(ns.Routing.Static.IPv6) != 1 {
		t.Errorf("namespace static routes = v4:%+v v6:%+v", ns.Routing.Static.IPv4, ns.Routing.Static.IPv6)
	}

	// Host routes advertised via BGP network statements (VTEP preserved).
	if len(ns.Routing.BGP.AF.UcastV4.Network) != 2 ||
		ns.Routing.BGP.AF.UcastV4.Network[1].Prefix != "10.0.0.5/32" {
		t.Errorf("v4 network statements = %+v", ns.Routing.BGP.AF.UcastV4.Network)
	}
	if ns.Routing.BGP.AF.UcastV6 == nil || len(ns.Routing.BGP.AF.UcastV6.Network) != 1 ||
		ns.Routing.BGP.AF.UcastV6.Network[0].Prefix != "fd00:200::5/128" {
		t.Errorf("v6 network statements = %+v", ns.Routing.BGP.AF.UcastV6)
	}

	// The XML must carry the advertised prefix.
	out, err := xml.Marshal(ns)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), "<network><ip-prefix>10.0.0.5/32</ip-prefix></network>") {
		t.Errorf("rendered XML missing v4 network statement\n%s", out)
	}
}

// TestApplyGlobalWorkloadPortsNoBGP verifies host-route advertisement is skipped
// gracefully when no underlay BGP session is composed.
func TestApplyGlobalWorkloadPortsNoBGP(t *testing.T) {
	ns := &Namespace{Interfaces: &Interfaces{}, Routing: &Routing{}}
	if _, err := applyGlobalWorkloadPorts(ns, WorkloadPort{
		IfName:     craPortIfName,
		HostRoutes: []string{"10.0.0.5/32"},
	}); err != nil {
		t.Fatalf("applyGlobalWorkloadPorts: %v", err)
	}
	if len(ns.Interfaces.Infras) != 1 {
		t.Errorf("infra not rendered without BGP: %+v", ns.Interfaces.Infras)
	}
}

// TestConvStaticRouteInterfaceNextHop verifies the NNC interface (on-link)
// next-hop is rendered as `next-hop <ifname>`.
func TestConvStaticRouteInterfaceNextHop(t *testing.T) {
	ifname := craPortIfName
	got := LayerBGP{}.convStaticRoute(v1alpha1.StaticRoute{
		Prefix:  "10.0.0.5/32",
		NextHop: &v1alpha1.NextHop{Interface: &ifname},
	})
	if got.Destination != "10.0.0.5/32" {
		t.Errorf("destination = %q", got.Destination)
	}
	if len(got.NextHops) != 1 || got.NextHops[0].NextHop != craPortIfName {
		t.Errorf("next-hop = %+v, want craport0", got.NextHops)
	}
	if got.NextHops[0].VRF != nil {
		t.Errorf("interface next-hop must not set VRF, got %v", got.NextHops[0].VRF)
	}
}

// TestLayer2AttachTrunkPortsXML verifies a trunked port renders one base
// interface shared by every domain it carries, one vlan interface per member
// (carrying the workload-side id) and one bridge slave per domain — including
// across an IRB Layer2 whose bridge lives in a tenant VRF.
func TestLayer2AttachTrunkPortsXML(t *testing.T) {
	ns := &Namespace{Name: "hbn", Interfaces: &Interfaces{}}
	l := &Layer2{vrouter: &VRouter{}, ns: ns}

	// The green domain lives in the netns itself, the red one is an IRB domain
	// whose bridge sits in a tenant VRF.
	vrfIntfs := &Interfaces{}
	green := &Bridge{Name: "l2.100"}
	red := &Bridge{Name: "l2.200"}
	port := v1alpha1.AttachedPort{
		Interface:  "cra012345",
		PortWiring: v1alpha1.PortWiring{Transport: v1alpha1.PortTransportVeth},
		MTU:        9000,
	}

	greenPort := port
	greenPort.VLAN = 100
	if err := l.attachPorts(&InfoL2{vni: 100, mtu: 9000,
		attachedPorts: []v1alpha1.AttachedPort{greenPort}}, green, ns.Interfaces); err != nil {
		t.Fatalf("attachPorts (green): %v", err)
	}
	// Translated: the fabric-side 200 is carried as 3000 on the workload side.
	redPort := port
	redPort.VLAN = 3000
	if err := l.attachPorts(&InfoL2{vni: 200, mtu: 9000,
		attachedPorts: []v1alpha1.AttachedPort{redPort}}, red, vrfIntfs); err != nil {
		t.Fatalf("attachPorts (red): %v", err)
	}

	// The shared port is declared exactly once, in the namespace's own set.
	if len(ns.Interfaces.Infras) != 1 || ns.Interfaces.Infras[0].Name != "cra012345" {
		t.Fatalf("expected exactly one shared infra interface, got %+v", ns.Interfaces.Infras)
	}
	if len(vrfIntfs.Infras) != 0 {
		t.Errorf("trunked port must not be declared in the IRB VRF, got %+v", vrfIntfs.Infras)
	}
	if len(ns.Interfaces.VLANs) != 2 {
		t.Fatalf("expected one vlan interface per member, got %+v", ns.Interfaces.VLANs)
	}
	for i, want := range []struct {
		name   string
		vlanID int
	}{{"cra012345.100", 100}, {"cra012345.3000", 3000}} {
		got := ns.Interfaces.VLANs[i]
		if got.Name != want.name || got.VlanID != want.vlanID || got.LinkInterface != "cra012345" {
			t.Errorf("vlan interface %d = %+v, want %s on cra012345 with vlan-id %d",
				i, got, want.name, want.vlanID)
		}
		// The sub-interface is sized like the port the attachment asked for,
		// not left at the platform default.
		if got.MTU == nil || *got.MTU != 9000 {
			t.Errorf("vlan interface %s mtu = %v, want 9000", want.name, got.MTU)
		}
	}
	// The sub-interfaces are slaved, never the raw port: untagged and unlisted
	// tags are not forwarded anywhere.
	if len(green.Slaves) != 1 || green.Slaves[0].Name != "cra012345.100" {
		t.Errorf("green slaves = %+v, want cra012345.100", green.Slaves)
	}
	if len(red.Slaves) != 1 || red.Slaves[0].Name != "cra012345.3000" {
		t.Errorf("red slaves = %+v, want cra012345.3000", red.Slaves)
	}

	out, err := xml.Marshal(ns.Interfaces)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, w := range []string{
		"<name>cra012345.100</name><vlan-id>100</vlan-id><link-interface>cra012345</link-interface>",
		"<name>cra012345.3000</name><vlan-id>3000</vlan-id><link-interface>cra012345</link-interface>",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("rendered interfaces XML missing %q\nfull XML:\n%s", w, got)
		}
	}
}

// TestLayer2AttachTrunkRejectsOverlongName guards the kernel interface-name
// limit the fast path materialises the vlan interface under.
func TestLayer2AttachTrunkRejectsOverlongName(t *testing.T) {
	ns := &Namespace{Name: "hbn", Interfaces: &Interfaces{}}
	l := &Layer2{vrouter: &VRouter{}, ns: ns}
	err := l.attachPorts(&InfoL2{vni: 100, attachedPorts: []v1alpha1.AttachedPort{{
		Interface:  "cra0123456789",
		PortWiring: v1alpha1.PortWiring{Transport: v1alpha1.PortTransportVeth},
		VLAN:       4094,
	}}}, &Bridge{Name: "l2.100"}, ns.Interfaces)
	if err == nil {
		t.Fatal("expected an error for an over-long trunk sub-interface name")
	}
}
