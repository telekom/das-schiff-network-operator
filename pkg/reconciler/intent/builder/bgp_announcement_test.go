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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// ---------------------------------------------------------------------------
// BGPPeeringBuilder tests
// ---------------------------------------------------------------------------

func TestBGPPeeringBuilder_Name(t *testing.T) {
	b := NewBGPPeeringBuilder()
	if b.Name() != "bgppeering" {
		t.Errorf("expected name 'bgppeering', got %q", b.Name())
	}
}

func TestBGPPeeringBuilder_EmptyData(t *testing.T) {
	b := NewBGPPeeringBuilder()
	result, err := b.Build(context.Background(), &resolver.ResolvedData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestBGPPeeringBuilder_ListenRange(t *testing.T) { //nolint:funlen // table-driven test
	b := NewBGPPeeringBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"transfer-net": {
				Name: "transfer-net",
				Spec: nc.NetworkSpec{
					VLAN: ptr(int32(100)),
					VNI:  ptr(int32(10100)),
					IPv4: &nc.IPNetwork{CIDR: "10.100.0.0/24"},
					IPv6: &nc.IPNetwork{CIDR: "fd00:100::/64"},
				},
			},
		},
		VRFs: map[string]*resolver.ResolvedVRF{
			"prod-vrf": {
				Name: "prod-vrf",
				Spec: nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")},
			},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dc-dest": {
				Name:    "dc-dest",
				Spec:    nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
				VRFSpec: &nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")},
			},
		},
		RawDestinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "dc-dest", Labels: map[string]string{"env": "prod"}},
				Spec:       nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
			},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "transfer-l2a"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef: "transfer-net",
					Destinations: &metav1.LabelSelector{
						MatchLabels: map[string]string{"env": "prod"},
					},
				},
			},
		},
		BGPPeerings: []nc.BGPPeering{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "listen-peer"},
				Spec: nc.BGPPeeringSpec{
					Mode: nc.BGPPeeringModeListenRange,
					Ref: nc.BGPPeeringRef{
						AttachmentRef: ptr("transfer-l2a"),
						InboundRefs:   []string{"my-inbound"},
					},
					WorkloadAS: ptr(int64(65100)),
				},
			},
		},
	}

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

	// FabricVRFs key is the backbone VRF name (Spec.VRF), not the CRD name.
	fvrf, ok := contrib.FabricVRFs["prod"]
	if !ok {
		t.Fatal("expected FabricVRF 'prod'")
	}

	// Dual-stack network → 2 peers (one IPv4, one IPv6).
	if len(fvrf.BGPPeers) != 2 {
		t.Fatalf("expected 2 BGPPeers (IPv4 + IPv6), got %d", len(fvrf.BGPPeers))
	}

	// Check IPv4 peer.
	p4 := fvrf.BGPPeers[0]
	if p4.ListenRange == nil || *p4.ListenRange != "10.100.0.0/24" {
		t.Errorf("expected IPv4 ListenRange '10.100.0.0/24', got %v", p4.ListenRange)
	}
	if p4.RemoteASN != 65100 {
		t.Errorf("expected RemoteASN 65100, got %d", p4.RemoteASN)
	}
	if p4.IPv4 == nil {
		t.Error("expected IPv4 address family on IPv4 peer")
	}
	if p4.IPv6 != nil {
		t.Error("expected nil IPv6 address family on IPv4 peer")
	}

	// Check IPv6 peer.
	p6 := fvrf.BGPPeers[1]
	if p6.ListenRange == nil || *p6.ListenRange != "fd00:100::/64" {
		t.Errorf("expected IPv6 ListenRange 'fd00:100::/64', got %v", p6.ListenRange)
	}
	if p6.RemoteASN != 65100 {
		t.Errorf("expected RemoteASN 65100, got %d", p6.RemoteASN)
	}
	if p6.IPv6 == nil {
		t.Error("expected IPv6 address family on IPv6 peer")
	}
	if p6.IPv4 != nil {
		t.Error("expected nil IPv4 address family on IPv6 peer")
	}
}

func TestBGPPeeringBuilder_LoopbackPeer(t *testing.T) {
	b := NewBGPPeeringBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}},
		},
		BGPPeerings: []nc.BGPPeering{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "loopback-peer"},
				Spec: nc.BGPPeeringSpec{
					Mode: nc.BGPPeeringModeLoopbackPeer,
					Ref: nc.BGPPeeringRef{
						InboundRefs: []string{"my-inbound"},
					},
					WorkloadAS: ptr(int64(65200)),
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 node contributions, got %d", len(result))
	}

	for _, nodeName := range []string{"node-1", "node-2"} {
		contrib, ok := result[nodeName]
		if !ok {
			t.Fatalf("expected contribution for %s", nodeName)
		}

		if contrib.ClusterVRF == nil {
			t.Fatalf("expected ClusterVRF for %s", nodeName)
		}

		if len(contrib.ClusterVRF.BGPPeers) != 1 {
			t.Fatalf("expected 1 BGPPeer on ClusterVRF for %s, got %d", nodeName, len(contrib.ClusterVRF.BGPPeers))
		}

		peer := contrib.ClusterVRF.BGPPeers[0]
		if peer.RemoteASN != 65200 {
			t.Errorf("expected RemoteASN 65200 for %s, got %d", nodeName, peer.RemoteASN)
		}

		// Default: dual-stack address families.
		if peer.IPv4 == nil {
			t.Errorf("expected IPv4 address family for %s", nodeName)
		}
		if peer.IPv6 == nil {
			t.Errorf("expected IPv6 address family for %s", nodeName)
		}
	}
}

func TestBGPPeeringBuilder_UnknownMode(t *testing.T) {
	b := NewBGPPeeringBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		BGPPeerings: []nc.BGPPeering{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-peer"},
				Spec: nc.BGPPeeringSpec{
					Mode: nc.BGPPeeringMode("invalid"),
					Ref:  nc.BGPPeeringRef{InboundRefs: []string{"x"}},
				},
			},
		},
	}

	_, err := b.Build(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for unknown mode, got nil")
	}
	if !strings.Contains(err.Error(), "unknown mode") {
		t.Errorf("expected 'unknown mode' in error, got: %v", err)
	}
}

func TestBGPPeeringBuilder_MissingAttachmentRef(t *testing.T) {
	b := NewBGPPeeringBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		BGPPeerings: []nc.BGPPeering{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "missing-ref"},
				Spec: nc.BGPPeeringSpec{
					Mode: nc.BGPPeeringModeListenRange,
					Ref:  nc.BGPPeeringRef{InboundRefs: []string{"x"}},
					// AttachmentRef intentionally omitted.
				},
			},
		},
	}

	_, err := b.Build(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for missing attachmentRef, got nil")
	}
	if !strings.Contains(err.Error(), "attachmentRef") {
		t.Errorf("expected 'attachmentRef' in error, got: %v", err)
	}
}

func TestBGPPeeringBuilder_WithBFD(t *testing.T) {
	b := NewBGPPeeringBuilder()

	holdDuration := metav1.Duration{Duration: 90 * time.Second}
	keepaliveDuration := metav1.Duration{Duration: 30 * time.Second}

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		BGPPeerings: []nc.BGPPeering{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bfd-peer"},
				Spec: nc.BGPPeeringSpec{
					Mode: nc.BGPPeeringModeLoopbackPeer,
					Ref: nc.BGPPeeringRef{
						InboundRefs: []string{"my-inbound"},
					},
					WorkloadAS:    ptr(int64(65300)),
					HoldTime:      &holdDuration,
					KeepaliveTime: &keepaliveDuration,
					EnableBFD:     ptr(true),
					BFDProfile: &nc.BFDProfile{
						MinInterval: 300,
					},
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	contrib := result["node-1"]
	if contrib == nil || contrib.ClusterVRF == nil {
		t.Fatal("expected ClusterVRF contribution for node-1")
	}

	peer := contrib.ClusterVRF.BGPPeers[0]

	if peer.RemoteASN != 65300 {
		t.Errorf("expected RemoteASN 65300, got %d", peer.RemoteASN)
	}
	if peer.HoldTime == nil || peer.HoldTime.Duration != 90*time.Second {
		t.Errorf("expected HoldTime 90s, got %v", peer.HoldTime)
	}
	if peer.KeepaliveTime == nil || peer.KeepaliveTime.Duration != 30*time.Second {
		t.Errorf("expected KeepaliveTime 30s, got %v", peer.KeepaliveTime)
	}
	if peer.BFDProfile == nil {
		t.Fatal("expected BFDProfile to be set when EnableBFD=true")
	}
	if peer.BFDProfile.MinInterval != 300 {
		t.Errorf("expected BFDProfile MinInterval 300, got %d", peer.BFDProfile.MinInterval)
	}
}

// ---------------------------------------------------------------------------
// AnnouncementBuilder tests (now a no-op — community logic converged into usage builders).
// ---------------------------------------------------------------------------

func TestAnnouncementBuilder_Name(t *testing.T) {
	b := NewAnnouncementBuilder()
	if b.Name() != "announcement" {
		t.Errorf("expected name 'announcement', got %q", b.Name())
	}
}

func TestAnnouncementBuilder_EmptyData(t *testing.T) {
	b := NewAnnouncementBuilder()
	result, err := b.Build(context.Background(), &resolver.ResolvedData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil contributions, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// findMatchingAP tests.
// ---------------------------------------------------------------------------

func TestFindMatchingAP_NoMatch(t *testing.T) {
	data := &resolver.ResolvedData{
		VRFs: map[string]*resolver.ResolvedVRF{
			"prod-vrf": {Name: "prod-vrf", Spec: nc.VRFSpec{VRF: "prod"}},
		},
		AnnouncementPolicies: []nc.AnnouncementPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "ap1"},
				Spec: nc.AnnouncementPolicySpec{
					VRFRef:   "prod-vrf",
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "staging"}},
				},
			},
		},
	}
	ap, err := findMatchingAP(map[string]string{"env": "prod"}, "prod", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ap != nil {
		t.Fatalf("expected nil AP, got %v", ap)
	}
}

func TestFindMatchingAP_SingleMatch(t *testing.T) {
	data := &resolver.ResolvedData{
		VRFs: map[string]*resolver.ResolvedVRF{
			"prod-vrf": {Name: "prod-vrf", Spec: nc.VRFSpec{VRF: "prod"}},
		},
		AnnouncementPolicies: []nc.AnnouncementPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "ap1"},
				Spec: nc.AnnouncementPolicySpec{
					VRFRef: "prod-vrf",
					HostRoutes: &nc.RouteAnnouncementConfig{
						Communities: []string{"65000:100"},
					},
				},
			},
		},
	}
	ap, err := findMatchingAP(map[string]string{"env": "prod"}, "prod", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ap == nil || ap.Name != "ap1" {
		t.Fatalf("expected ap1, got %v", ap)
	}
}

func TestFindMatchingAP_MultipleMatchError(t *testing.T) {
	data := &resolver.ResolvedData{
		VRFs: map[string]*resolver.ResolvedVRF{
			"prod-vrf": {Name: "prod-vrf", Spec: nc.VRFSpec{VRF: "prod"}},
		},
		AnnouncementPolicies: []nc.AnnouncementPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "ap1"},
				Spec:       nc.AnnouncementPolicySpec{VRFRef: "prod-vrf"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "ap2"},
				Spec:       nc.AnnouncementPolicySpec{VRFRef: "prod-vrf"},
			},
		},
	}
	_, err := findMatchingAP(map[string]string{}, "prod", data)
	if err == nil {
		t.Fatal("expected error for multiple matching APs, got nil")
	}
	if !strings.Contains(err.Error(), "multiple AnnouncementPolicies match") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// cidrFilterItems tests (unit-level).
// ---------------------------------------------------------------------------

func TestCidrFilterItems_NoAP(t *testing.T) {
	items := cidrFilterItems("10.1.0.0/24", 32, 31, nil)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Action.Type != networkv1alpha1.Accept {
		t.Errorf("expected Accept, got %q", items[0].Action.Type)
	}
	if items[0].Matcher.Prefix.Le == nil || *items[0].Matcher.Prefix.Le != 32 {
		t.Errorf("expected Le=32, got %v", items[0].Matcher.Prefix.Le)
	}
}

func TestCidrFilterItems_WithHostCommunities(t *testing.T) {
	ap := &nc.AnnouncementPolicy{
		Spec: nc.AnnouncementPolicySpec{
			HostRoutes: &nc.RouteAnnouncementConfig{
				Communities: []string{"65000:100"},
			},
		},
	}
	items := cidrFilterItems("10.1.0.0/24", 32, 31, ap)
	// Should have 2 items: host-route (ge=32,le=32) + non-host (le=31).
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Host route item.
	host := items[0]
	if host.Matcher.Prefix.Ge == nil || *host.Matcher.Prefix.Ge != 32 {
		t.Errorf("host: expected Ge=32, got %v", host.Matcher.Prefix.Ge)
	}
	if host.Action.ModifyRoute == nil || len(host.Action.ModifyRoute.AddCommunities) != 1 {
		t.Errorf("host: expected 1 community, got %v", host.Action.ModifyRoute)
	}

	// Non-host item (aggregate enabled by default → accept).
	nonHost := items[1]
	if nonHost.Matcher.Prefix.Le == nil || *nonHost.Matcher.Prefix.Le != 31 {
		t.Errorf("non-host: expected Le=31, got %v", nonHost.Matcher.Prefix.Le)
	}
	if nonHost.Action.Type != networkv1alpha1.Accept {
		t.Errorf("non-host: expected Accept, got %q", nonHost.Action.Type)
	}
}

func TestCidrFilterItems_AggregateDisabled(t *testing.T) {
	ap := &nc.AnnouncementPolicy{
		Spec: nc.AnnouncementPolicySpec{
			Aggregate: &nc.AggregateConfig{Enabled: ptr(false)},
		},
	}
	items := cidrFilterItems("10.1.0.0/24", 32, 31, ap)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Host route item (plain accept — no host communities configured).
	if items[0].Action.Type != networkv1alpha1.Accept {
		t.Errorf("host: expected Accept, got %q", items[0].Action.Type)
	}
	if items[0].Action.ModifyRoute != nil {
		t.Errorf("host: expected nil ModifyRoute")
	}

	// Non-host item (reject).
	if items[1].Action.Type != networkv1alpha1.Reject {
		t.Errorf("non-host: expected Reject, got %q", items[1].Action.Type)
	}
}

func TestCidrFilterItems_AggregateCommunities(t *testing.T) {
	ap := &nc.AnnouncementPolicy{
		Spec: nc.AnnouncementPolicySpec{
			HostRoutes: &nc.RouteAnnouncementConfig{
				Communities: []string{"65000:100"},
			},
			Aggregate: &nc.AggregateConfig{
				Enabled:     ptr(true),
				Communities: []string{"65000:300"},
			},
		},
	}
	items := cidrFilterItems("10.1.0.0/24", 32, 31, ap)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Host route with host community.
	if items[0].Action.ModifyRoute == nil || items[0].Action.ModifyRoute.AddCommunities[0] != "65000:100" {
		t.Errorf("host: expected community 65000:100, got %v", items[0].Action.ModifyRoute)
	}

	// Aggregate with aggregate community.
	if items[1].Action.ModifyRoute == nil || items[1].Action.ModifyRoute.AddCommunities[0] != "65000:300" {
		t.Errorf("agg: expected community 65000:300, got %v", items[1].Action.ModifyRoute)
	}
}

// ---------------------------------------------------------------------------
// addressFilterItems tests.
// ---------------------------------------------------------------------------

func TestAddressFilterItems_WithAP(t *testing.T) {
	ap := &nc.AnnouncementPolicy{
		Spec: nc.AnnouncementPolicySpec{
			HostRoutes: &nc.RouteAnnouncementConfig{
				Communities: []string{"65000:200"},
			},
		},
	}
	items := addressFilterItems([]string{"10.1.0.1/32", "fd00::1/128"}, ap)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	for _, item := range items {
		if item.Action.ModifyRoute == nil || len(item.Action.ModifyRoute.AddCommunities) != 1 {
			t.Errorf("expected 1 community on address item, got %v", item.Action.ModifyRoute)
		}
	}
}

func TestAddressFilterItems_NoAP(t *testing.T) {
	items := addressFilterItems([]string{"10.1.0.1/32"}, nil)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Action.ModifyRoute != nil {
		t.Errorf("expected nil ModifyRoute without AP, got %v", items[0].Action.ModifyRoute)
	}
}

// ---------------------------------------------------------------------------
// Converged L2A + AP integration test.
// ---------------------------------------------------------------------------

func TestL2ABuilder_WithAnnouncementPolicy(t *testing.T) {
	b := NewL2ABuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		VRFs: map[string]*resolver.ResolvedVRF{
			"prod-vrf": {
				Name: "prod-vrf",
				Spec: nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")},
			},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"prod-net": {
				Spec: nc.NetworkSpec{
					VNI:  ptr(int32(10100)),
					IPv4: &nc.IPNetwork{CIDR: "10.1.0.0/24"},
					IPv6: &nc.IPNetwork{CIDR: "fd00:1::/64"},
				},
			},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dest-prod": {
				VRFSpec: &nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")},
				Spec:    nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
			},
		},
		RawDestinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "dest-prod", Labels: map[string]string{"role": "gateway"}},
				Spec:       nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
			},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "l2a-prod", Labels: map[string]string{"tier": "frontend"}},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef:   "prod-net",
					Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "gateway"}},
				},
			},
		},
		AnnouncementPolicies: []nc.AnnouncementPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "host-policy"},
				Spec: nc.AnnouncementPolicySpec{
					VRFRef: "prod-vrf",
					HostRoutes: &nc.RouteAnnouncementConfig{
						Communities: []string{"65000:100", "65000:200"},
					},
					Aggregate: &nc.AggregateConfig{
						Enabled:     ptr(true),
						Communities: []string{"65000:300"},
					},
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	contrib := result["node-1"]
	fvrf, ok := contrib.FabricVRFs["prod"]
	if !ok {
		t.Fatal("expected FabricVRF 'prod'")
	}

	filter := fvrf.EVPNExportFilter
	if filter == nil {
		t.Fatal("expected EVPNExportFilter to be set")
	}

	// With AP: 2 items per address family (host + non-host) × 2 AF = 4 items.
	if len(filter.Items) != 4 {
		t.Fatalf("expected 4 filter items (2 per AF), got %d", len(filter.Items))
	}

	// IPv4 host route item.
	item4Host := filter.Items[0]
	if item4Host.Matcher.Prefix.Ge == nil || *item4Host.Matcher.Prefix.Ge != 32 {
		t.Errorf("expected Ge=32 for IPv4 host route, got %v", item4Host.Matcher.Prefix.Ge)
	}
	if item4Host.Action.ModifyRoute == nil || len(item4Host.Action.ModifyRoute.AddCommunities) != 2 {
		t.Errorf("expected 2 host communities, got %v", item4Host.Action.ModifyRoute)
	}

	// IPv4 non-host item (aggregate with community).
	item4Agg := filter.Items[1]
	if item4Agg.Matcher.Prefix.Le == nil || *item4Agg.Matcher.Prefix.Le != 31 {
		t.Errorf("expected Le=31 for IPv4 aggregate, got %v", item4Agg.Matcher.Prefix.Le)
	}
	if item4Agg.Action.ModifyRoute == nil || item4Agg.Action.ModifyRoute.AddCommunities[0] != "65000:300" {
		t.Errorf("expected aggregate community 65000:300, got %v", item4Agg.Action.ModifyRoute)
	}

	// VRFImport should have plain items (no communities).
	if len(fvrf.VRFImports) == 0 {
		t.Fatal("expected VRFImports")
	}
	for _, item := range fvrf.VRFImports[0].Filter.Items {
		if item.Action.ModifyRoute != nil {
			t.Errorf("VRFImport item should have no ModifyRoute, got %v", item.Action.ModifyRoute)
		}
	}

	// Aggregate static route should be present.
	if len(fvrf.StaticRoutes) == 0 {
		t.Fatal("expected aggregate static routes")
	}
}

// ---------------------------------------------------------------------------
// Converged Inbound + AP integration test.
// ---------------------------------------------------------------------------

func TestInboundBuilder_WithAnnouncementPolicy(t *testing.T) {
	b := NewInboundBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		VRFs: map[string]*resolver.ResolvedVRF{
			"prod-vrf": {
				Name: "prod-vrf",
				Spec: nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")},
			},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"prod-net": {
				Spec: nc.NetworkSpec{
					VNI:  ptr(int32(10100)),
					IPv4: &nc.IPNetwork{CIDR: "10.1.0.0/24"},
				},
			},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dest-prod": {
				VRFSpec: &nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")},
				Spec:    nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
			},
		},
		RawDestinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "dest-prod", Labels: map[string]string{"role": "gateway"}},
				Spec:       nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
			},
		},
		Inbounds: []nc.Inbound{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "ib-test", Labels: map[string]string{"svc": "web"}},
				Spec: nc.InboundSpec{
					NetworkRef:   "prod-net",
					Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "gateway"}},
					Addresses:    &nc.AddressAllocation{IPv4: []string{"10.1.0.5/32"}},
				},
			},
		},
		AnnouncementPolicies: []nc.AnnouncementPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "host-policy"},
				Spec: nc.AnnouncementPolicySpec{
					VRFRef: "prod-vrf",
					HostRoutes: &nc.RouteAnnouncementConfig{
						Communities: []string{"65000:500"},
					},
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	contrib := result["node-1"]
	fvrf := contrib.FabricVRFs["prod"]
	if fvrf.EVPNExportFilter == nil {
		t.Fatal("expected EVPNExportFilter")
	}

	// Inbound addresses are host routes — should have community.
	if len(fvrf.EVPNExportFilter.Items) != 1 {
		t.Fatalf("expected 1 EVPN filter item, got %d", len(fvrf.EVPNExportFilter.Items))
	}
	item := fvrf.EVPNExportFilter.Items[0]
	if item.Action.ModifyRoute == nil || item.Action.ModifyRoute.AddCommunities[0] != "65000:500" {
		t.Errorf("expected community 65000:500 on inbound address, got %v", item.Action.ModifyRoute)
	}

	// VRFImport should have plain items.
	if len(fvrf.VRFImports) > 0 && len(fvrf.VRFImports[0].Filter.Items) > 0 {
		for _, vi := range fvrf.VRFImports[0].Filter.Items {
			if vi.Action.ModifyRoute != nil {
				t.Errorf("VRFImport item should have no ModifyRoute")
			}
		}
	}
}

func TestMergeFilter_BaseDefaultActionWins(t *testing.T) {
	base := &networkv1alpha1.Filter{
		DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		Items: []networkv1alpha1.FilterItem{
			{Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept}},
		},
	}
	addition := &networkv1alpha1.Filter{
		DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
		Items: []networkv1alpha1.FilterItem{
			{Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept}},
		},
	}

	merged := mergeFilter(base, addition)

	if merged.DefaultAction.Type != networkv1alpha1.Reject {
		t.Errorf("base DefaultAction must be preserved: expected Reject, got %q", merged.DefaultAction.Type)
	}
	if len(merged.Items) != 2 {
		t.Errorf("expected 2 merged items, got %d", len(merged.Items))
	}
}
