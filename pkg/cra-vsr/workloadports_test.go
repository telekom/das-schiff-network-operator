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
	if err := applyWorkloadPorts(vrf, WorkloadPort{
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
	if err := applyWorkloadPorts(vrf, WorkloadPort{}); err == nil {
		t.Errorf("expected error for empty ifname")
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

	err := applyWorkloadPorts(vrf, WorkloadPort{
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
	if err := applyWorkloadPorts(vrf); err != nil {
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

	if err := applyGlobalWorkloadPorts(ns, WorkloadPort{
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
	if err := applyGlobalWorkloadPorts(ns, WorkloadPort{
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
