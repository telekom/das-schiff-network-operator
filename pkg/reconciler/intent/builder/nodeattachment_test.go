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
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// baseNodeAttachmentData returns a minimal ResolvedData for NodeAttachment tests.
func baseNodeAttachmentData() *resolver.ResolvedData {
	return &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "node-1",
					Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""},
				},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeInternalIP, Address: "10.0.1.10"},
						{Type: corev1.NodeInternalIP, Address: "fd00::10"},
						{Type: corev1.NodeHostName, Address: "node-1"},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "node-2",
					Labels: map[string]string{"node-role.kubernetes.io/worker": ""},
				},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeInternalIP, Address: "10.0.1.11"},
					},
				},
			},
		},
		VRFs: map[string]*resolver.ResolvedVRF{
			"san-vrf": {Name: "san-vrf", Spec: nc.VRFSpec{VRF: "san", VNI: ptr(int32(5000)), RouteTarget: ptr("65000:5000")}},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dest-san": {
				Name:    "dest-san",
				Spec:    nc.DestinationSpec{VRFRef: ptr("san-vrf"), Prefixes: []string{"172.16.0.0/16", "fd01::/64"}},
				VRFSpec: &nc.VRFSpec{VRF: "san", VNI: ptr(int32(5000)), RouteTarget: ptr("65000:5000")},
			},
		},
		RawDestinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "dest-san", Labels: map[string]string{"type": "san-storage"}},
				Spec:       nc.DestinationSpec{VRFRef: ptr("san-vrf"), Prefixes: []string{"172.16.0.0/16", "fd01::/64"}},
			},
		},
	}
}

func TestNodeAttachmentBuilder_Name(t *testing.T) {
	b := NewNodeAttachmentBuilder()
	if b.Name() != "nodeattachment" {
		t.Errorf("expected name 'nodeattachment', got %q", b.Name())
	}
}

func TestNodeAttachmentBuilder_EmptyData(t *testing.T) {
	b := NewNodeAttachmentBuilder()
	result, err := b.Build(context.Background(), &resolver.ResolvedData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestNodeAttachmentBuilder_BasicNodeAttachment(t *testing.T) {
	data := baseNodeAttachmentData()
	data.NodeAttachments = []nc.NodeAttachment{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "san"},
			Spec: nc.NodeAttachmentSpec{
				VRFRef:       "san-vrf",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "san-storage"}},
			},
		},
	}

	b := NewNodeAttachmentBuilder()
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All nodes should get contributions (no nodeSelector).
	if len(result) != 2 {
		t.Fatalf("expected 2 node contributions, got %d", len(result))
	}

	// Check node-1 (has both IPv4 and IPv6).
	contrib := result["node-1"]
	if contrib == nil {
		t.Fatal("expected contribution for node-1")
	}

	fvrf, ok := contrib.FabricVRFs["san"]
	if !ok {
		t.Fatal("expected FabricVRF 'san'")
	}

	if fvrf.VNI != 5000 {
		t.Errorf("expected VNI 5000, got %d", fvrf.VNI)
	}
	if len(fvrf.EVPNImportRouteTargets) != 1 || fvrf.EVPNImportRouteTargets[0] != "65000:5000" {
		t.Errorf("unexpected route targets: %v", fvrf.EVPNImportRouteTargets)
	}

	// EVPN export filter should have 2 items (IPv4 + IPv6 host routes).
	if fvrf.EVPNExportFilter == nil {
		t.Fatal("expected EVPNExportFilter")
	}
	if len(fvrf.EVPNExportFilter.Items) != 2 {
		t.Fatalf("expected 2 EVPN export items, got %d", len(fvrf.EVPNExportFilter.Items))
	}
	assertFilterItemPrefix(t, fvrf.EVPNExportFilter.Items[0], "10.0.1.10/32")
	assertFilterItemPrefix(t, fvrf.EVPNExportFilter.Items[1], "fd00::10/128")

	// FabricVRF's cluster VRF import should also have the node IPs.
	if len(fvrf.VRFImports) != 1 {
		t.Fatalf("expected 1 FabricVRF VRFImport, got %d", len(fvrf.VRFImports))
	}
	if fvrf.VRFImports[0].FromVRF != "cluster" {
		t.Errorf("expected FabricVRF VRFImport from 'cluster', got %q", fvrf.VRFImports[0].FromVRF)
	}
	if len(fvrf.VRFImports[0].Filter.Items) != 2 {
		t.Fatalf("expected 2 FabricVRF VRFImport filter items, got %d", len(fvrf.VRFImports[0].Filter.Items))
	}

	// ClusterVRF should have a VRFImport from san accepting destination prefixes.
	if contrib.ClusterVRF == nil {
		t.Fatal("expected ClusterVRF")
	}
	if len(contrib.ClusterVRF.VRFImports) != 1 {
		t.Fatalf("expected 1 ClusterVRF VRFImport, got %d", len(contrib.ClusterVRF.VRFImports))
	}
	clusterImport := contrib.ClusterVRF.VRFImports[0]
	if clusterImport.FromVRF != "san" {
		t.Errorf("expected ClusterVRF VRFImport from 'san', got %q", clusterImport.FromVRF)
	}
	if len(clusterImport.Filter.Items) != 2 {
		t.Fatalf("expected 2 ClusterVRF import filter items (storage prefixes), got %d", len(clusterImport.Filter.Items))
	}
	// Verify the storage prefixes are imported.
	assertFilterItemPrefix(t, clusterImport.Filter.Items[0], "172.16.0.0/16")
	assertFilterItemPrefix(t, clusterImport.Filter.Items[1], "fd01::/64")

	// Check node-2 (has only IPv4).
	contrib2 := result["node-2"]
	if contrib2 == nil {
		t.Fatal("expected contribution for node-2")
	}
	fvrf2 := contrib2.FabricVRFs["san"]
	if fvrf2.EVPNExportFilter == nil || len(fvrf2.EVPNExportFilter.Items) != 1 {
		t.Fatalf("expected 1 EVPN export item for node-2, got %v", fvrf2.EVPNExportFilter)
	}
	assertFilterItemPrefix(t, fvrf2.EVPNExportFilter.Items[0], "10.0.1.11/32")
}

func TestNodeAttachmentBuilder_NodeSelectorFiltering(t *testing.T) {
	data := baseNodeAttachmentData()
	data.NodeAttachments = []nc.NodeAttachment{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "san"},
			Spec: nc.NodeAttachmentSpec{
				VRFRef:       "san-vrf",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "san-storage"}},
				NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"node-role.kubernetes.io/control-plane": ""}},
			},
		},
	}

	b := NewNodeAttachmentBuilder()
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only node-1 matches the selector.
	if len(result) != 1 {
		t.Fatalf("expected 1 node contribution, got %d", len(result))
	}
	if _, ok := result["node-1"]; !ok {
		t.Error("expected contribution for node-1")
	}
	if _, ok := result["node-2"]; ok {
		t.Error("did not expect contribution for node-2")
	}
}

func TestNodeAttachmentBuilder_IPFamilies(t *testing.T) {
	const (
		node1IPv4 = "10.0.1.10/32"
		node1IPv6 = "fd00::10/128"
		node2IPv4 = "10.0.1.11/32"
	)
	tests := []struct {
		name       string
		ipFamilies []corev1.IPFamily
		want       map[string][]string
	}{
		{
			name: "omitted exports all",
			want: map[string][]string{
				"node-1": {node1IPv4, node1IPv6},
				"node-2": {node2IPv4},
			},
		},
		{
			name:       "empty exports all",
			ipFamilies: []corev1.IPFamily{},
			want: map[string][]string{
				"node-1": {node1IPv4, node1IPv6},
				"node-2": {node2IPv4},
			},
		},
		{
			name:       "IPv4 only",
			ipFamilies: []corev1.IPFamily{corev1.IPv4Protocol},
			want: map[string][]string{
				"node-1": {node1IPv4},
				"node-2": {node2IPv4},
			},
		},
		{
			name:       "IPv6 only preserves imports on IPv4-only nodes",
			ipFamilies: []corev1.IPFamily{corev1.IPv6Protocol},
			want: map[string][]string{
				"node-1": {node1IPv6},
				"node-2": {},
			},
		},
		{
			name:       "explicit dual stack",
			ipFamilies: []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol},
			want: map[string][]string{
				"node-1": {node1IPv4, node1IPv6},
				"node-2": {node2IPv4},
			},
		},
		{
			name:       "family order does not affect exports",
			ipFamilies: []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol},
			want: map[string][]string{
				"node-1": {node1IPv4, node1IPv6},
				"node-2": {node2IPv4},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := baseNodeAttachmentData()
			data.Nodes[0].Status.Addresses = append(data.Nodes[0].Status.Addresses,
				corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "192.0.2.10"},
				corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "2001:db8::10"},
				corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: ""},
			)
			data.NodeAttachments = []nc.NodeAttachment{{
				ObjectMeta: metav1.ObjectMeta{Name: "san"},
				Spec: nc.NodeAttachmentSpec{
					VRFRef:       "san-vrf",
					Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "san-storage"}},
					IPFamilies:   tt.ipFamilies,
				},
			}}

			result, err := NewNodeAttachmentBuilder().Build(context.Background(), data)
			require.NoError(t, err)
			require.Len(t, result, len(tt.want))
			for nodeName, prefixes := range tt.want {
				contrib := result[nodeName]
				require.NotNil(t, contrib, "expected contribution for %s", nodeName)
				fvrf, ok := contrib.FabricVRFs["san"]
				require.True(t, ok, "expected FabricVRF for destination imports on %s", nodeName)
				require.Equal(t, uint32(5000), fvrf.VNI)
				require.Equal(t, []string{"65000:5000"}, fvrf.EVPNImportRouteTargets)
				require.NotNil(t, fvrf.EVPNExportFilter)
				require.Equal(t, networkv1alpha1.Reject, fvrf.EVPNExportFilter.DefaultAction.Type)
				require.Len(t, fvrf.EVPNExportFilter.Items, len(prefixes))
				require.Len(t, fvrf.VRFImports, 1)
				require.Equal(t, "cluster", fvrf.VRFImports[0].FromVRF)
				require.Equal(t, networkv1alpha1.Reject, fvrf.VRFImports[0].Filter.DefaultAction.Type)
				require.Len(t, fvrf.VRFImports[0].Filter.Items, len(prefixes))
				for i, prefix := range prefixes {
					assertFilterItemPrefix(t, fvrf.EVPNExportFilter.Items[i], prefix)
					assertFilterItemPrefix(t, fvrf.VRFImports[0].Filter.Items[i], prefix)
				}
				require.NotNil(t, contrib.ClusterVRF)
				require.Len(t, contrib.ClusterVRF.VRFImports, 1)
				items := contrib.ClusterVRF.VRFImports[0].Filter.Items
				require.Len(t, items, 2, "expected both destination families for %s", nodeName)
				assertFilterItemPrefix(t, items[0], "172.16.0.0/16")
				assertFilterItemPrefix(t, items[1], "fd01::/64")
			}
		})
	}
}

func TestNodeAttachmentBuilder_NoNodesMatched(t *testing.T) {
	data := baseNodeAttachmentData()
	data.NodeAttachments = []nc.NodeAttachment{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "san"},
			Spec: nc.NodeAttachmentSpec{
				VRFRef:       "san-vrf",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "san-storage"}},
				NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"non-existent-label": "true"}},
			},
		},
	}

	b := NewNodeAttachmentBuilder()
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestNodeAttachmentBuilder_ClusterVRFImport(t *testing.T) {
	data := baseNodeAttachmentData()
	data.NodeAttachments = []nc.NodeAttachment{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "san"},
			Spec: nc.NodeAttachmentSpec{
				VRFRef:       "san-vrf",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "san-storage"}},
				NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"node-role.kubernetes.io/control-plane": ""}},
			},
		},
	}

	b := NewNodeAttachmentBuilder()
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	contrib := result["node-1"]
	if contrib == nil {
		t.Fatal("expected contribution for node-1")
	}

	// ClusterVRF should have a VRFImport from san with destination prefixes.
	if contrib.ClusterVRF == nil {
		t.Fatal("expected ClusterVRF")
	}
	if len(contrib.ClusterVRF.VRFImports) != 1 {
		t.Fatalf("expected 1 ClusterVRF VRFImport, got %d", len(contrib.ClusterVRF.VRFImports))
	}
	imp := contrib.ClusterVRF.VRFImports[0]
	if imp.FromVRF != "san" {
		t.Errorf("expected import from 'san', got %q", imp.FromVRF)
	}
	if imp.Filter.DefaultAction.Type != networkv1alpha1.Reject {
		t.Errorf("expected default reject, got %q", imp.Filter.DefaultAction.Type)
	}

	// Should accept storage prefixes.
	if len(imp.Filter.Items) != 2 {
		t.Fatalf("expected 2 filter items, got %d", len(imp.Filter.Items))
	}
	assertFilterItemPrefix(t, imp.Filter.Items[0], "172.16.0.0/16")
	assertFilterItemPrefix(t, imp.Filter.Items[1], "fd01::/64")

	// SBR should NOT produce anything for NodeAttachments (no SBR involvement).
	sbrB := NewSBRBuilder()
	sbrResult, err := sbrB.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("SBR unexpected error: %v", err)
	}
	if len(sbrResult) != 0 {
		t.Errorf("expected no SBR contributions for NodeAttachment, got %d", len(sbrResult))
	}
}

// assertFilterItemPrefix checks that a FilterItem matches a specific prefix.
func assertFilterItemPrefix(t *testing.T, item networkv1alpha1.FilterItem, expected string) {
	t.Helper()
	if item.Matcher.Prefix == nil {
		t.Errorf("expected prefix matcher, got nil")
		return
	}
	if item.Matcher.Prefix.Prefix != expected {
		t.Errorf("expected prefix %q, got %q", expected, item.Matcher.Prefix.Prefix)
	}
	if item.Action.Type != networkv1alpha1.Accept {
		t.Errorf("expected Accept action, got %q", item.Action.Type)
	}
}
