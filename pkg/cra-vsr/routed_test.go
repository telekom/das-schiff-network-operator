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

func TestBuildRoutedVRF(t *testing.T) {
	vrf, err := BuildRoutedVRF("cluster", 100, RoutedPort{
		IfName:    "cra0123456789ab",
		GatewayV4: "169.254.100.100/32",
		GatewayV6: "fd00:7:caa5:1::/128",
		HostRoutes: []string{
			"10.0.0.5/32",
			"fd00:200::5/128",
		},
	})
	if err != nil {
		t.Fatalf("BuildRoutedVRF: %v", err)
	}

	if vrf.Name != "cluster" || vrf.TableID != 100 {
		t.Errorf("vrf = %q table %d, want cluster/100", vrf.Name, vrf.TableID)
	}
	if vrf.Routing == nil || vrf.Routing.NCOperation != Merge {
		t.Fatalf("expected merge routing operation")
	}

	// Infrastructure interface: port infra-<ifname> + on-link gateways.
	if len(vrf.Interfaces.Infras) != 1 {
		t.Fatalf("expected 1 infra interface, got %d", len(vrf.Interfaces.Infras))
	}
	infra := vrf.Interfaces.Infras[0]
	if infra.Name != "cra0123456789ab" {
		t.Errorf("infra name = %q", infra.Name)
	}
	if infra.Port == nil || *infra.Port != "infra-cra0123456789ab" {
		t.Errorf("infra port = %v, want infra-cra0123456789ab", infra.Port)
	}
	if infra.IPv4 == nil || infra.IPv4.IPAddresses[0].IP != "169.254.100.100/32" {
		t.Errorf("infra ipv4 = %v", infra.IPv4)
	}
	if infra.IPv6 == nil || infra.IPv6.IPAddresses[0].IP != "fd00:7:caa5:1::/128" {
		t.Errorf("infra ipv6 = %v", infra.IPv6)
	}

	// Host routes split by family, next-hop = ifname (on-link).
	if len(vrf.Routing.Static.IPv4) != 1 || vrf.Routing.Static.IPv4[0].Destination != "10.0.0.5/32" {
		t.Errorf("ipv4 static = %+v", vrf.Routing.Static.IPv4)
	}
	if got := vrf.Routing.Static.IPv4[0].NextHops[0].NextHop; got != "cra0123456789ab" {
		t.Errorf("ipv4 next-hop = %q, want ifname", got)
	}
	if len(vrf.Routing.Static.IPv6) != 1 || vrf.Routing.Static.IPv6[0].Destination != "fd00:200::5/128" {
		t.Errorf("ipv6 static = %+v", vrf.Routing.Static.IPv6)
	}
	if got := vrf.Routing.Static.IPv6[0].NextHops[0].NextHop; got != "cra0123456789ab" {
		t.Errorf("ipv6 next-hop = %q, want ifname", got)
	}
}

func TestBuildRoutedVRFXML(t *testing.T) {
	vrf, err := BuildRoutedVRF("cluster", 0, RoutedPort{
		IfName:     "craport0",
		GatewayV4:  "169.254.100.100/32",
		GatewayV6:  "fd00:7:caa5:1::/128",
		HostRoutes: []string{"10.0.0.5/32", "fd00:200::5/128"},
	})
	if err != nil {
		t.Fatalf("BuildRoutedVRF: %v", err)
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
	// vrfTable <= 0 must omit table-id.
	if strings.Contains(got, "<table-id>") {
		t.Errorf("expected table-id omitted for vrfTable<=0, got:\n%s", got)
	}
}

func TestBuildRoutedVRFErrors(t *testing.T) {
	if _, err := BuildRoutedVRF("", 0); err == nil {
		t.Errorf("expected error for empty vrf name")
	}
	if _, err := BuildRoutedVRF("cluster", 0, RoutedPort{}); err == nil {
		t.Errorf("expected error for empty ifname")
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

	err := applyRoutedPorts(vrf, RoutedPort{
		IfName:     "craport0",
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
	if vrf.Routing.Static.IPv4[1].NextHops[0].NextHop != "craport0" {
		t.Errorf("routed v4 next-hop = %q", vrf.Routing.Static.IPv4[1].NextHops[0].NextHop)
	}
	if len(vrf.Routing.Static.IPv6) != 1 || vrf.Routing.Static.IPv6[0].NextHops[0].NextHop != "craport0" {
		t.Errorf("routed v6 static = %+v", vrf.Routing.Static.IPv6)
	}

	// Empty port list is a no-op.
	before := len(vrf.Interfaces.Infras)
	if err := applyRoutedPorts(vrf); err != nil {
		t.Fatalf("applyRoutedPorts(empty): %v", err)
	}
	if len(vrf.Interfaces.Infras) != before {
		t.Errorf("empty applyRoutedPorts mutated vrf")
	}
}

// TestConvStaticRouteInterfaceNextHop verifies the NNC interface (on-link)
// next-hop is rendered as `next-hop <ifname>`.
func TestConvStaticRouteInterfaceNextHop(t *testing.T) {
	ifname := "craport0"
	got := LayerBGP{}.convStaticRoute(v1alpha1.StaticRoute{
		Prefix:  "10.0.0.5/32",
		NextHop: &v1alpha1.NextHop{Interface: &ifname},
	})
	if got.Destination != "10.0.0.5/32" {
		t.Errorf("destination = %q", got.Destination)
	}
	if len(got.NextHops) != 1 || got.NextHops[0].NextHop != "craport0" {
		t.Errorf("next-hop = %+v, want craport0", got.NextHops)
	}
	if got.NextHops[0].VRF != nil {
		t.Errorf("interface next-hop must not set VRF, got %v", got.NextHops[0].VRF)
	}
}
