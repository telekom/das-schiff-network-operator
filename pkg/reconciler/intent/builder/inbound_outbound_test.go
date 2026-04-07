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

package builder

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

const testOutboundPrefix = "10.100.0.0/24"

// baseInboundData returns a minimal ResolvedData with one node, one Network,
// one VRF-backed Destination, and a matching RawDestination for label-selector matching.
func baseInboundData() *resolver.ResolvedData {
	return &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		VRFs: map[string]*resolver.ResolvedVRF{
			"gw-vrf": {Name: "gw-vrf", Spec: nc.VRFSpec{VRF: "gateway", VNI: ptr(int32(3000)), RouteTarget: ptr("65000:3000")}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-1": {Name: "net-1", Spec: nc.NetworkSpec{
				IPv4: &nc.IPNetwork{CIDR: "10.200.0.0/24"},
			}},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dest-gw": {
				Name:    "dest-gw",
				Spec:    nc.DestinationSpec{VRFRef: ptr("gw-vrf")},
				VRFSpec: &nc.VRFSpec{VRF: "gateway", VNI: ptr(int32(3000)), RouteTarget: ptr("65000:3000")},
			},
		},
		RawDestinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "dest-gw", Labels: map[string]string{"type": "gw"}},
				Spec:       nc.DestinationSpec{VRFRef: ptr("gw-vrf")},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// InboundBuilder tests
// ---------------------------------------------------------------------------

func TestInboundBuilder_Name(t *testing.T) {
	b := NewInboundBuilder()
	if b.Name() != "inbound" {
		t.Errorf("expected name 'inbound', got %q", b.Name())
	}
}

func TestInboundBuilder_EmptyData(t *testing.T) {
	b := NewInboundBuilder()
	result, err := b.Build(context.Background(), &resolver.ResolvedData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestInboundBuilder_BasicInbound(t *testing.T) {
	data := baseInboundData()
	data.Inbounds = []nc.Inbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "my-inbound"},
			Spec: nc.InboundSpec{
				NetworkRef:   "net-1",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
				Addresses:    &nc.AddressAllocation{IPv4: []string{"10.250.1.0/24"}, IPv6: []string{"fd00::1/128"}},
			},
		},
	}

	b := NewInboundBuilder()
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 node contribution, got %d", len(result))
	}

	contrib, ok := result["node-1"]
	if !ok {
		t.Fatal("expected contribution for node-1")
	}

	fvrf, ok := contrib.FabricVRFs["gw-vrf"]
	if !ok {
		t.Fatal("expected FabricVRF 'gw-vrf'")
	}

	// VNI and RouteTargets from the resolved VRFSpec.
	if fvrf.VNI != 3000 {
		t.Errorf("expected VNI 3000, got %d", fvrf.VNI)
	}
	if len(fvrf.EVPNImportRouteTargets) != 1 || fvrf.EVPNImportRouteTargets[0] != "65000:3000" {
		t.Errorf("unexpected EVPNImportRouteTargets: %v", fvrf.EVPNImportRouteTargets)
	}

	// EVPN export filter should contain items for both addresses.
	if fvrf.EVPNExportFilter == nil {
		t.Fatal("expected non-nil EVPNExportFilter")
	}
	if len(fvrf.EVPNExportFilter.Items) != 2 {
		t.Fatalf("expected 2 EVPN export filter items, got %d", len(fvrf.EVPNExportFilter.Items))
	}
	// First item is IPv4.
	if fvrf.EVPNExportFilter.Items[0].Matcher.Prefix.Prefix != "10.250.1.0/24" {
		t.Errorf("expected prefix 10.250.1.0/24, got %q", fvrf.EVPNExportFilter.Items[0].Matcher.Prefix.Prefix)
	}
	if *fvrf.EVPNExportFilter.Items[0].Matcher.Prefix.Le != 32 {
		t.Errorf("expected IPv4 Le=32, got %d", *fvrf.EVPNExportFilter.Items[0].Matcher.Prefix.Le)
	}
	// Second item is IPv6.
	if fvrf.EVPNExportFilter.Items[1].Matcher.Prefix.Prefix != "fd00::1/128" {
		t.Errorf("expected prefix fd00::1/128, got %q", fvrf.EVPNExportFilter.Items[1].Matcher.Prefix.Prefix)
	}
	if *fvrf.EVPNExportFilter.Items[1].Matcher.Prefix.Le != 128 {
		t.Errorf("expected IPv6 Le=128, got %d", *fvrf.EVPNExportFilter.Items[1].Matcher.Prefix.Le)
	}

	// VRFImports filter should mirror the same items.
	if len(fvrf.VRFImports) == 0 {
		t.Fatal("expected at least one VRFImport")
	}
	if len(fvrf.VRFImports[0].Filter.Items) != 2 {
		t.Errorf("expected 2 VRFImport filter items, got %d", len(fvrf.VRFImports[0].Filter.Items))
	}

	// Redistribute connected for the Network CIDR.
	if fvrf.Redistribute == nil {
		t.Fatal("expected non-nil Redistribute")
	}
	if fvrf.Redistribute.Connected == nil {
		t.Fatal("expected non-nil Redistribute.Connected")
	}
	if len(fvrf.Redistribute.Connected.Items) != 1 {
		t.Fatalf("expected 1 redistribute item, got %d", len(fvrf.Redistribute.Connected.Items))
	}
	if fvrf.Redistribute.Connected.Items[0].Matcher.Prefix.Prefix != "10.200.0.0/24" {
		t.Errorf("expected redistribute prefix 10.200.0.0/24, got %q", fvrf.Redistribute.Connected.Items[0].Matcher.Prefix.Prefix)
	}
	if fvrf.Redistribute.Connected.DefaultAction.Type != networkv1alpha1.Reject {
		t.Errorf("expected redistribute default action Reject, got %q", fvrf.Redistribute.Connected.DefaultAction.Type)
	}
}

func TestInboundBuilder_UnknownNetwork(t *testing.T) {
	data := baseInboundData()
	data.Inbounds = []nc.Inbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-inbound"},
			Spec: nc.InboundSpec{
				NetworkRef:   "nonexistent",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
			},
		},
	}

	b := NewInboundBuilder()
	_, err := b.Build(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for unknown Network reference, got nil")
	}
	if !strings.Contains(err.Error(), "unknown Network") {
		t.Errorf("expected 'unknown Network' in error, got: %v", err)
	}
}

func TestInboundBuilder_NoDestinations(t *testing.T) {
	data := baseInboundData()
	data.Inbounds = []nc.Inbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "no-dest-inbound"},
			Spec: nc.InboundSpec{
				NetworkRef: "net-1",
				Addresses:  &nc.AddressAllocation{IPv4: []string{"10.250.1.0/24"}},
				// No Destinations selector.
			},
		},
	}

	b := NewInboundBuilder()
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions when no destinations, got %d", len(result))
	}
}

func TestInboundBuilder_DualStackRedistribute(t *testing.T) {
	data := baseInboundData()
	// Override the Network with both IPv4 and IPv6.
	data.Networks["net-1"] = &resolver.ResolvedNetwork{
		Name: "net-1",
		Spec: nc.NetworkSpec{
			IPv4: &nc.IPNetwork{CIDR: "10.200.0.0/24"},
			IPv6: &nc.IPNetwork{CIDR: "fd00:200::/64"},
		},
	}
	data.Inbounds = []nc.Inbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "ds-inbound"},
			Spec: nc.InboundSpec{
				NetworkRef:   "net-1",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
				Addresses:    &nc.AddressAllocation{IPv4: []string{"10.250.1.1/32"}},
			},
		},
	}

	b := NewInboundBuilder()
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	contrib := result["node-1"]
	if contrib == nil {
		t.Fatal("expected contribution for node-1")
	}

	fvrf, ok := contrib.FabricVRFs["gw-vrf"]
	if !ok {
		t.Fatal("expected FabricVRF 'gw-vrf'")
	}

	if fvrf.Redistribute == nil || fvrf.Redistribute.Connected == nil {
		t.Fatal("expected non-nil Redistribute.Connected")
	}
	if len(fvrf.Redistribute.Connected.Items) != 2 {
		t.Fatalf("expected 2 redistribute items (IPv4+IPv6), got %d", len(fvrf.Redistribute.Connected.Items))
	}

	prefixes := make(map[string]bool)
	for _, item := range fvrf.Redistribute.Connected.Items {
		prefixes[item.Matcher.Prefix.Prefix] = true
	}
	if !prefixes["10.200.0.0/24"] {
		t.Error("expected IPv4 redistribute prefix 10.200.0.0/24")
	}
	if !prefixes["fd00:200::/64"] {
		t.Error("expected IPv6 redistribute prefix fd00:200::/64")
	}
}

// ---------------------------------------------------------------------------
// OutboundBuilder tests
// ---------------------------------------------------------------------------

func TestOutboundBuilder_Name(t *testing.T) {
	b := NewOutboundBuilder()
	if b.Name() != "outbound" {
		t.Errorf("expected name 'outbound', got %q", b.Name())
	}
}

func TestOutboundBuilder_EmptyData(t *testing.T) {
	b := NewOutboundBuilder()
	result, err := b.Build(context.Background(), &resolver.ResolvedData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestOutboundBuilder_BasicOutbound(t *testing.T) {
	data := baseInboundData()
	data.Outbounds = []nc.Outbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "my-outbound"},
			Spec: nc.OutboundSpec{
				NetworkRef:   "net-1",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
				Addresses:    &nc.AddressAllocation{IPv4: []string{testOutboundPrefix}},
			},
		},
	}

	b := NewOutboundBuilder()
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 node contribution, got %d", len(result))
	}

	contrib, ok := result["node-1"]
	if !ok {
		t.Fatal("expected contribution for node-1")
	}

	fvrf, ok := contrib.FabricVRFs["gw-vrf"]
	if !ok {
		t.Fatal("expected FabricVRF 'gw-vrf'")
	}

	if fvrf.VNI != 3000 {
		t.Errorf("expected VNI 3000, got %d", fvrf.VNI)
	}

	// EVPN export filter items for the address.
	if fvrf.EVPNExportFilter == nil {
		t.Fatal("expected non-nil EVPNExportFilter")
	}
	if len(fvrf.EVPNExportFilter.Items) != 1 {
		t.Fatalf("expected 1 EVPN export filter item, got %d", len(fvrf.EVPNExportFilter.Items))
	}
	if fvrf.EVPNExportFilter.Items[0].Matcher.Prefix.Prefix != testOutboundPrefix {
		t.Errorf("expected prefix %s, got %q", testOutboundPrefix, fvrf.EVPNExportFilter.Items[0].Matcher.Prefix.Prefix)
	}

	// VRFImport filter items mirror the same.
	if len(fvrf.VRFImports) == 0 {
		t.Fatal("expected at least one VRFImport")
	}
	if len(fvrf.VRFImports[0].Filter.Items) != 1 {
		t.Errorf("expected 1 VRFImport filter item, got %d", len(fvrf.VRFImports[0].Filter.Items))
	}

	// Outbound does NOT produce redistribute.
	if fvrf.Redistribute != nil {
		t.Error("expected nil Redistribute for Outbound")
	}
}

func TestOutboundBuilder_UnknownNetwork(t *testing.T) {
	data := baseInboundData()
	data.Outbounds = []nc.Outbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-outbound"},
			Spec: nc.OutboundSpec{
				NetworkRef:   "nonexistent",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
			},
		},
	}

	b := NewOutboundBuilder()
	_, err := b.Build(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for unknown Network reference, got nil")
	}
	if !strings.Contains(err.Error(), "unknown Network") {
		t.Errorf("expected 'unknown Network' in error, got: %v", err)
	}
}

func TestOutboundBuilder_NoDestinations(t *testing.T) {
	data := baseInboundData()
	data.Outbounds = []nc.Outbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "no-dest-outbound"},
			Spec: nc.OutboundSpec{
				NetworkRef: "net-1",
				Addresses:  &nc.AddressAllocation{IPv4: []string{testOutboundPrefix}},
				// No Destinations selector.
			},
		},
	}

	b := NewOutboundBuilder()
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions when no destinations, got %d", len(result))
	}
}
