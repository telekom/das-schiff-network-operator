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

package cni

import (
	"net"
	"reflect"
	"testing"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
)

func TestPodIdentity(t *testing.T) {
	ns, name := podIdentity("IgnoreUnknown=1;K8S_POD_NAMESPACE=demo;K8S_POD_NAME=vm-launcher;K8S_POD_INFRA_CONTAINER_ID=abc")
	if ns != "demo" || name != "vm-launcher" {
		t.Fatalf("unexpected identity ns=%q name=%q", ns, name)
	}

	ns, name = podIdentity("")
	if ns != "" || name != "" {
		t.Fatalf("expected empty identity, got ns=%q name=%q", ns, name)
	}
}

func TestHostRoutes(t *testing.T) {
	result := &current.Result{
		IPs: []*current.IPConfig{
			{Address: net.IPNet{IP: net.ParseIP("10.201.0.10"), Mask: net.CIDRMask(32, 32)}},
			{Address: net.IPNet{IP: net.ParseIP("fd00:201::10"), Mask: net.CIDRMask(128, 128)}},
		},
	}
	got := hostRoutes(result)
	want := []string{"10.201.0.10/32", "fd00:201::10/128"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hostRoutes = %v, want %v", got, want)
	}
}

func TestNormalizeHostPrefixes(t *testing.T) {
	result := &current.Result{
		IPs: []*current.IPConfig{
			{Address: net.IPNet{IP: net.ParseIP("10.201.0.10"), Mask: net.CIDRMask(24, 32)}},
			{Address: net.IPNet{IP: net.ParseIP("fd00:201::10"), Mask: net.CIDRMask(64, 128)}},
			{Address: net.IPNet{IP: net.ParseIP("10.201.0.11"), Mask: net.CIDRMask(32, 32)}},
		},
	}
	normalizeHostPrefixes(result)
	want := []string{"10.201.0.10/32", "fd00:201::10/128", "10.201.0.11/32"}
	for i, ip := range result.IPs {
		if got := ip.Address.String(); got != want[i] {
			t.Errorf("IPs[%d] = %s, want %s", i, got, want[i])
		}
	}
}

func TestRewriteRoutedResult(t *testing.T) {
	gwV4, gwV6 := net.ParseIP("169.254.1.1"), net.ParseIP("fe80::1")
	// What host-local hands back: the pool gateway on every address and the
	// routes from its config, none of which the routed datapath installs.
	result := &current.Result{
		IPs: []*current.IPConfig{
			{Address: net.IPNet{IP: net.ParseIP("10.201.0.10"), Mask: net.CIDRMask(32, 32)}, Gateway: net.ParseIP("10.201.0.1")},
			{Address: net.IPNet{IP: net.ParseIP("fd00:201::10"), Mask: net.CIDRMask(128, 128)}, Gateway: net.ParseIP("fd00:201::1")},
		},
		Routes: []*types.Route{
			{Dst: net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}, GW: net.ParseIP("10.201.0.1")},
			{Dst: net.IPNet{IP: net.ParseIP("192.168.0.0"), Mask: net.CIDRMask(16, 32)}},
		},
	}
	rewriteRoutedResult(result, gwV4, gwV6)

	if !result.IPs[0].Gateway.Equal(gwV4) || !result.IPs[1].Gateway.Equal(gwV6) {
		t.Errorf("gateways = %s / %s, want the link-local %s / %s",
			result.IPs[0].Gateway, result.IPs[1].Gateway, gwV4, gwV6)
	}
	if len(result.Routes) != 2 {
		t.Fatalf("routes = %v, want exactly the two on-link defaults", result.Routes)
	}
	if result.Routes[0].Dst.String() != "0.0.0.0/0" || !result.Routes[0].GW.Equal(gwV4) {
		t.Errorf("v4 route = %s via %s, want 0.0.0.0/0 via %s", result.Routes[0].Dst.String(), result.Routes[0].GW, gwV4)
	}
	if result.Routes[1].Dst.String() != "::/0" || !result.Routes[1].GW.Equal(gwV6) {
		t.Errorf("v6 route = %s via %s, want ::/0 via %s", result.Routes[1].Dst.String(), result.Routes[1].GW, gwV6)
	}

	// A single-family allocation only advertises that family's default.
	v4only := &current.Result{IPs: result.IPs[:1]}
	rewriteRoutedResult(v4only, gwV4, gwV6)
	if len(v4only.Routes) != 1 || v4only.Routes[0].Dst.String() != "0.0.0.0/0" {
		t.Errorf("v4-only routes = %v, want only 0.0.0.0/0", v4only.Routes)
	}
}

func TestAddRequestCarriesOnlyPresentGatewayFamilies(t *testing.T) {
	conf := &NetConf{VRF: "tenant"}
	args := &skel.CmdArgs{ContainerID: "cid", Args: "K8S_POD_NAMESPACE=demo;K8S_POD_NAME=vm"}
	gwV4, gwV6 := net.ParseIP("169.254.1.1"), net.ParseIP("fe80::1")
	v4 := &current.IPConfig{Address: net.IPNet{IP: net.ParseIP("10.201.0.10"), Mask: net.CIDRMask(32, 32)}}
	v6 := &current.IPConfig{Address: net.IPNet{IP: net.ParseIP("fd00:201::10"), Mask: net.CIDRMask(128, 128)}}

	tests := []struct {
		name           string
		ips            []*current.IPConfig
		wantV4, wantV6 string
	}{
		{name: "ipv4 only", ips: []*current.IPConfig{v4}, wantV4: "169.254.1.1/32"},
		{name: "ipv6 only", ips: []*current.IPConfig{v6}, wantV6: "fe80::1/128"},
		{name: "dual stack", ips: []*current.IPConfig{v4, v6}, wantV4: "169.254.1.1/32", wantV6: "fe80::1/128"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := addRequest(conf, args, "cra0cid", gwV4, gwV6, &current.Result{IPs: tc.ips})
			if req.GetPodNamespace() != "demo" || req.GetPodName() != "vm" || req.GetVrf() != "tenant" {
				t.Fatalf("unexpected identity in %+v", req)
			}
			if got := req.GetPort().GetGatewayV4(); got != tc.wantV4 {
				t.Errorf("gateway_v4 = %q, want %q", got, tc.wantV4)
			}
			if got := req.GetPort().GetGatewayV6(); got != tc.wantV6 {
				t.Errorf("gateway_v6 = %q, want %q", got, tc.wantV6)
			}
			if got := len(req.GetPort().GetHostRoutes()); got != len(tc.ips) {
				t.Errorf("host routes = %d, want %d", got, len(tc.ips))
			}
		})
	}
}

func TestAddRequestL2ModeCarriesOnlyLayer2References(t *testing.T) {
	args := &skel.CmdArgs{ContainerID: "cid", Args: "K8S_POD_NAMESPACE=demo;K8S_POD_NAME=vm"}
	vlan := uint16(200)
	conf := &NetConf{
		AttachMode: AttachModeL2,
		Layer2Trunk: []Layer2TrunkMember{
			{Layer2AttachmentRef: Layer2AttachmentRef{Name: "green"}},
			{Layer2AttachmentRef: Layer2AttachmentRef{Name: "red"}, VLAN: &vlan},
		},
		MTU: 9000,
	}

	// In L2 mode the caller passes no gateways; the result may carry IPAM
	// addresses (optional) that must not turn into routed payload.
	result := &current.Result{IPs: []*current.IPConfig{
		{Address: net.IPNet{IP: net.ParseIP("10.201.0.10"), Mask: net.CIDRMask(24, 32)}},
	}}
	req := addRequest(conf, args, "cra0cid", nil, nil, result)

	port := req.GetPort()
	if req.GetVrf() != "" || port.GetGatewayV4() != "" || port.GetGatewayV6() != "" || len(port.GetHostRoutes()) != 0 {
		t.Fatalf("L2 request unexpectedly carries routed payload: %+v", req)
	}
	if port.GetMtu() != 9000 {
		t.Errorf("mtu = %d, want 9000", port.GetMtu())
	}
	if req.GetLayer2AttachmentRef() != nil {
		t.Errorf("unexpected access ref %+v on a trunk", req.GetLayer2AttachmentRef())
	}
	members := req.GetLayer2Trunk()
	if len(members) != 2 || members[0].GetRef().GetName() != "green" || members[1].GetRef().GetName() != "red" {
		t.Fatalf("unexpected trunk members %+v", members)
	}
	if members[0].GetVlan() != 0 || members[1].GetVlan() != 200 {
		t.Errorf("vlans = %d/%d, want 0 (inherit)/200", members[0].GetVlan(), members[1].GetVlan())
	}
}
