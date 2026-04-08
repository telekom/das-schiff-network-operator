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

	// resolveL2AVRF returns the VRFRef name as the map key.
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
// AnnouncementBuilder tests
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
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestAnnouncementBuilder_BasicHostRoutes(t *testing.T) {
	b := NewAnnouncementBuilder()

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
		AnnouncementPolicies: []nc.AnnouncementPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "host-policy"},
				Spec: nc.AnnouncementPolicySpec{
					VRFRef: "prod-vrf",
					HostRoutes: &nc.RouteAnnouncementConfig{
						Communities: []string{"65000:100", "65000:200"},
					},
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

	contrib := result["node-1"]
	fvrf, ok := contrib.FabricVRFs["prod"]
	if !ok {
		t.Fatal("expected FabricVRF 'prod'")
	}

	filter := fvrf.EVPNExportFilter
	if filter == nil {
		t.Fatal("expected EVPNExportFilter to be set")
	}

	// Host routes → 2 filter items (/32 + /128).
	if len(filter.Items) != 2 {
		t.Fatalf("expected 2 filter items (IPv4 /32 + IPv6 /128), got %d", len(filter.Items))
	}

	// IPv4 host route item.
	item4 := filter.Items[0]
	if item4.Matcher.Prefix == nil || item4.Matcher.Prefix.Prefix != "0.0.0.0/0" {
		t.Errorf("expected IPv4 host route matcher prefix '0.0.0.0/0', got %v", item4.Matcher.Prefix)
	}
	if item4.Matcher.Prefix.Ge == nil || *item4.Matcher.Prefix.Ge != 32 {
		t.Errorf("expected Ge=32 for IPv4 host route, got %v", item4.Matcher.Prefix.Ge)
	}
	if item4.Matcher.Prefix.Le == nil || *item4.Matcher.Prefix.Le != 32 {
		t.Errorf("expected Le=32 for IPv4 host route, got %v", item4.Matcher.Prefix.Le)
	}
	if item4.Action.Type != networkv1alpha1.Accept {
		t.Errorf("expected Accept action, got %q", item4.Action.Type)
	}
	if item4.Action.ModifyRoute == nil || len(item4.Action.ModifyRoute.AddCommunities) != 2 {
		t.Errorf("expected 2 communities, got %v", item4.Action.ModifyRoute)
	}

	// IPv6 host route item.
	item6 := filter.Items[1]
	if item6.Matcher.Prefix == nil || item6.Matcher.Prefix.Prefix != "::/0" {
		t.Errorf("expected IPv6 host route matcher prefix '::/0', got %v", item6.Matcher.Prefix)
	}
	if item6.Matcher.Prefix.Ge == nil || *item6.Matcher.Prefix.Ge != 128 {
		t.Errorf("expected Ge=128 for IPv6 host route, got %v", item6.Matcher.Prefix.Ge)
	}

	if filter.DefaultAction.Type != networkv1alpha1.Reject {
		t.Errorf("expected DefaultAction Reject (base filter's deny-by-default is preserved by mergeFilter), got %q", filter.DefaultAction.Type)
	}
}

func TestAnnouncementBuilder_AggregateWithCommunities(t *testing.T) {
	b := NewAnnouncementBuilder()

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
		AnnouncementPolicies: []nc.AnnouncementPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "agg-policy"},
				Spec: nc.AnnouncementPolicySpec{
					VRFRef: "prod-vrf",
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
	fvrf := contrib.FabricVRFs["prod"]
	filter := fvrf.EVPNExportFilter
	if filter == nil {
		t.Fatal("expected EVPNExportFilter")
	}

	// Aggregate enabled with communities → 2 items (IPv4 ≤/31 + IPv6 ≤/127).
	if len(filter.Items) != 2 {
		t.Fatalf("expected 2 filter items for aggregate, got %d", len(filter.Items))
	}

	// IPv4 aggregate.
	agg4 := filter.Items[0]
	if agg4.Matcher.Prefix == nil || agg4.Matcher.Prefix.Prefix != "0.0.0.0/0" {
		t.Errorf("expected prefix '0.0.0.0/0', got %v", agg4.Matcher.Prefix)
	}
	if agg4.Matcher.Prefix.Le == nil || *agg4.Matcher.Prefix.Le != 31 {
		t.Errorf("expected Le=31 for IPv4 aggregate, got %v", agg4.Matcher.Prefix.Le)
	}
	if agg4.Matcher.Prefix.Ge != nil {
		t.Errorf("expected nil Ge for IPv4 aggregate, got %v", agg4.Matcher.Prefix.Ge)
	}
	if agg4.Action.Type != networkv1alpha1.Accept {
		t.Errorf("expected Accept action, got %q", agg4.Action.Type)
	}
	if agg4.Action.ModifyRoute == nil || len(agg4.Action.ModifyRoute.AddCommunities) != 1 {
		t.Errorf("expected 1 community on aggregate, got %v", agg4.Action.ModifyRoute)
	}

	// IPv6 aggregate.
	agg6 := filter.Items[1]
	if agg6.Matcher.Prefix == nil || agg6.Matcher.Prefix.Prefix != "::/0" {
		t.Errorf("expected prefix '::/0', got %v", agg6.Matcher.Prefix)
	}
	if agg6.Matcher.Prefix.Le == nil || *agg6.Matcher.Prefix.Le != 127 {
		t.Errorf("expected Le=127 for IPv6 aggregate, got %v", agg6.Matcher.Prefix.Le)
	}
}

func TestAnnouncementBuilder_AggregateDisabled(t *testing.T) {
	b := NewAnnouncementBuilder()

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
		AnnouncementPolicies: []nc.AnnouncementPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "reject-agg"},
				Spec: nc.AnnouncementPolicySpec{
					VRFRef: "prod-vrf",
					Aggregate: &nc.AggregateConfig{
						Enabled: ptr(false),
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
	filter := fvrf.EVPNExportFilter
	if filter == nil {
		t.Fatal("expected EVPNExportFilter")
	}

	// Aggregate disabled → 2 reject items (IPv4 ≤/31 + IPv6 ≤/127).
	if len(filter.Items) != 2 {
		t.Fatalf("expected 2 reject filter items, got %d", len(filter.Items))
	}

	for i, item := range filter.Items {
		if item.Action.Type != networkv1alpha1.Reject {
			t.Errorf("item[%d]: expected Reject action, got %q", i, item.Action.Type)
		}
		if item.Action.ModifyRoute != nil {
			t.Errorf("item[%d]: expected nil ModifyRoute on reject item", i)
		}
	}

	// Verify IPv4 reject matcher.
	if filter.Items[0].Matcher.Prefix == nil || *filter.Items[0].Matcher.Prefix.Le != 31 {
		t.Errorf("expected IPv4 reject Le=31, got %v", filter.Items[0].Matcher.Prefix)
	}
	// Verify IPv6 reject matcher.
	if filter.Items[1].Matcher.Prefix == nil || *filter.Items[1].Matcher.Prefix.Le != 127 {
		t.Errorf("expected IPv6 reject Le=127, got %v", filter.Items[1].Matcher.Prefix)
	}
}

func TestAnnouncementBuilder_UnknownVRF(t *testing.T) {
	b := NewAnnouncementBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		VRFs: map[string]*resolver.ResolvedVRF{},
		AnnouncementPolicies: []nc.AnnouncementPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-policy"},
				Spec: nc.AnnouncementPolicySpec{
					VRFRef: "nonexistent-vrf",
					HostRoutes: &nc.RouteAnnouncementConfig{
						Communities: []string{"65000:100"},
					},
				},
			},
		},
	}

	_, err := b.Build(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for unknown VRF, got nil")
	}
	if !strings.Contains(err.Error(), "unknown VRF") {
		t.Errorf("expected 'unknown VRF' in error, got: %v", err)
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
