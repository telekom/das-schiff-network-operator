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

// TestApplyRoutedPortsXML verifies that layering a routed port onto an existing
// VRF (as the VSR reconcile path does) renders the NETCONF constructs VSR
// expects. The VRF is composed first (mirroring LookupVRF) and then merged into.
func TestApplyRoutedPortsXML(t *testing.T) {
	vrf := &VRF{
		Name:       "cluster",
		Interfaces: &Interfaces{},
		Routing:    &Routing{NCOperation: Merge, Static: &StaticRouting{}},
	}
	if _, err := applyRoutedPorts(vrf, RoutedPort{
		IfName:     craPortIfName,
		GatewayV4:  "169.254.100.100/32",
		GatewayV6:  "fd00:7:caa5:1::/128",
		HostRoutes: []string{"10.0.0.5/32", "fd00:200::5/128"},
	}); err != nil {
		t.Fatalf("applyRoutedPorts: %v", err)
	}

	out, err := xml.Marshal(vrf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	// Assert the key NETCONF constructs are rendered as VSR expects.
	wants := []string{
		"<name>cluster</name>",
		"<infrastructure><name>craport0</name>",
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

func TestApplyRoutedPortsErrors(t *testing.T) {
	vrf := &VRF{Name: "cluster"}
	if _, err := applyRoutedPorts(vrf, RoutedPort{}); err == nil {
		t.Errorf("expected error for empty ifname")
	}
}

// TestApplyRoutedPortsVhostUserXML verifies a vhostuser routed port renders as a
// fpvhost interface (port fpvhost-<ifname>) instead of an infrastructure port,
// and returns a fast-path fpvhost virtual-port with the inverted socket mode.
func TestApplyRoutedPortsVhostUserXML(t *testing.T) {
	vrf := &VRF{
		Name:       "cluster",
		Interfaces: &Interfaces{},
		Routing:    &Routing{NCOperation: Merge, Static: &StaticRouting{}},
	}
	vports, err := applyRoutedPorts(vrf, RoutedPort{
		IfName:     craPortIfName,
		Transport:  v1alpha1.PortTransportVhostUser,
		SocketMode: "server", // workload server -> VSR client
		GatewayV4:  "169.254.100.100/32",
		HostRoutes: []string{"10.0.0.5/32"},
	})
	if err != nil {
		t.Fatalf("applyRoutedPorts: %v", err)
	}

	if len(vrf.Interfaces.Infras) != 0 {
		t.Errorf("vhostuser port must not render an infrastructure interface: %+v", vrf.Interfaces.Infras)
	}
	if len(vports) != 1 || vports[0].Name != "fpvhost-"+craPortIfName {
		t.Fatalf("expected one fpvhost virtual-port fpvhost-%s, got %+v", craPortIfName, vports)
	}
	if vports[0].SocketMode == nil || *vports[0].SocketMode != socketModeClient {
		t.Errorf("workload socket-mode server must invert to VSR client, got %v", vports[0].SocketMode)
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

// TestRegisterFpvhostVirtualPortsXML verifies the global system fast-path
// virtual-port subtree renders as VSR expects and de-duplicates by name.
func TestRegisterFpvhostVirtualPortsXML(t *testing.T) {
	vrouter := &VRouter{}
	registerFpvhostVirtualPorts(vrouter, []FpvhostVirtualPort{
		newFpvhostVirtualPort("craport0", "client"), // -> VSR server
	})
	// Duplicate registration must not add a second entry.
	registerFpvhostVirtualPorts(vrouter, []FpvhostVirtualPort{
		newFpvhostVirtualPort("craport0", "client"),
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
		"<socket-mode>server</socket-mode>",
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
			{Interface: "cra-vho", PortWiring: v1alpha1.PortWiring{Transport: v1alpha1.PortTransportVhostUser, SocketMode: "server"}},
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

// TestApplyRoutedPortsMerge verifies that routed ports layer onto an
// already-composed VRF (the NNC reconcile path) instead of replacing it: the
// pre-existing infra interface and static routes must be preserved.
func TestApplyRoutedPortsMerge(t *testing.T) {
	vrf := &VRF{
		Name:       "cluster",
		Interfaces: &Interfaces{Infras: []Infrastructure{{Name: "existing"}}},
		Routing: &Routing{
			Static: &StaticRouting{
				IPv4: []StaticRoute{{Destination: "10.9.9.0/24", NextHops: []NextHop{{NextHop: "blackhole"}}}},
			},
		},
	}

	_, err := applyRoutedPorts(vrf, RoutedPort{
		IfName:     craPortIfName,
		GatewayV4:  "169.254.1.1/32",
		HostRoutes: []string{"10.0.0.5/32", "fd00:200::5/128"},
	})
	if err != nil {
		t.Fatalf("applyRoutedPorts: %v", err)
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
	if _, err := applyRoutedPorts(vrf); err != nil {
		t.Fatalf("applyRoutedPorts(empty): %v", err)
	}
	if len(vrf.Interfaces.Infras) != before {
		t.Errorf("empty applyRoutedPorts mutated vrf")
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
