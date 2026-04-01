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

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptr[T any](v T) *T { return &v }

func TestL2ABuilder_Name(t *testing.T) {
	b := NewL2ABuilder()
	if b.Name() != "l2a" {
		t.Errorf("expected name 'l2a', got %q", b.Name())
	}
}

func TestL2ABuilder_EmptyData(t *testing.T) {
	b := NewL2ABuilder()
	result, err := b.Build(context.Background(), &resolver.ResolvedData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestL2ABuilder_BasicL2A(t *testing.T) {
	b := NewL2ABuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"role": "compute"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "node-2", Labels: map[string]string{"role": "compute"}}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"storage-net": {
				Name: "storage-net",
				Spec: nc.NetworkSpec{
					VLAN: ptr(int32(200)),
					VNI:  ptr(int32(10200)),
					IPv4: &nc.IPNetwork{CIDR: "192.168.200.0/24"},
				},
			},
		},
		Destinations: map[string]*resolver.ResolvedDestination{},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "storage-l2a"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef: "storage-net",
					MTU:        ptr(int32(9000)),
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should produce contributions for both nodes (no nodeSelector = all nodes).
	if len(result) != 2 {
		t.Fatalf("expected 2 node contributions, got %d", len(result))
	}

	for _, nodeName := range []string{"node-1", "node-2"} {
		contrib, ok := result[nodeName]
		if !ok {
			t.Fatalf("expected contribution for %s", nodeName)
		}

		l2, ok := contrib.Layer2s["200"]
		if !ok {
			t.Fatalf("expected Layer2 with key '200' for %s", nodeName)
		}
		if l2.VNI != 10200 {
			t.Errorf("expected VNI 10200, got %d", l2.VNI)
		}
		if l2.VLAN != 200 {
			t.Errorf("expected VLAN 200, got %d", l2.VLAN)
		}
		if l2.MTU != 9000 {
			t.Errorf("expected MTU 9000, got %d", l2.MTU)
		}
		if l2.IRB != nil {
			t.Error("expected nil IRB (no destinations)")
		}
	}
}

func TestL2ABuilder_WithNodeSelector(t *testing.T) {
	b := NewL2ABuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "compute-1", Labels: map[string]string{"role": "compute"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "control-1", Labels: map[string]string{"role": "control-plane"}}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"gpu-net": {
				Name: "gpu-net",
				Spec: nc.NetworkSpec{
					VLAN: ptr(int32(300)),
					VNI:  ptr(int32(10300)),
				},
			},
		},
		Destinations: map[string]*resolver.ResolvedDestination{},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu-l2a"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef: "gpu-net",
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"role": "compute"},
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
		t.Fatalf("expected 1 node contribution (only compute), got %d", len(result))
	}
	if _, ok := result["compute-1"]; !ok {
		t.Error("expected contribution for compute-1")
	}
	if _, ok := result["control-1"]; ok {
		t.Error("expected no contribution for control-1")
	}
}

func TestL2ABuilder_WithDestinationVRF(t *testing.T) {
	b := NewL2ABuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"tenant-net": {
				Name: "tenant-net",
				Spec: nc.NetworkSpec{
					VLAN: ptr(int32(100)),
					VNI:  ptr(int32(10100)),
					IPv4: &nc.IPNetwork{CIDR: "10.100.0.0/24"},
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
			"corp-dc": {
				Name:    "corp-dc",
				Spec:    nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
				VRFSpec: &nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")},
			},
		},
		RawDestinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "corp-dc",
					Labels: map[string]string{"env": "prod"},
				},
				Spec: nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
			},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "tenant-l2a"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef: "tenant-net",
					Destinations: &metav1.LabelSelector{
						MatchLabels: map[string]string{"env": "prod"},
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
	if contrib == nil {
		t.Fatal("expected contribution for node-1")
	}

	l2, ok := contrib.Layer2s["100"]
	if !ok {
		t.Fatal("expected Layer2 with key '100'")
	}

	// Should have IRB because destinations resolved to a VRF.
	if l2.IRB == nil {
		t.Fatal("expected IRB to be set when destinations have VRF")
	}
	if l2.IRB.VRF != "prod" {
		t.Errorf("expected IRB VRF 'prod', got %q", l2.IRB.VRF)
	}
	if len(l2.IRB.IPAddresses) != 1 || l2.IRB.IPAddresses[0] != "10.100.0.0/24" {
		t.Errorf("expected IRB IP [10.100.0.0/24], got %v", l2.IRB.IPAddresses)
	}

	// FabricVRF should have been created.
	fvrf, ok := contrib.FabricVRFs["prod"]
	if !ok {
		t.Fatal("expected FabricVRF 'prod'")
	}
	if fvrf.VNI != 5001 {
		t.Errorf("expected FabricVRF VNI 5001, got %d", fvrf.VNI)
	}
}

func TestL2ABuilder_DisableAnycast(t *testing.T) {
	b := NewL2ABuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-1": {
				Name: "net-1",
				Spec: nc.NetworkSpec{
					VLAN: ptr(int32(100)),
					VNI:  ptr(int32(10100)),
					IPv4: &nc.IPNetwork{CIDR: "10.0.0.0/24"},
				},
			},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dst-1": {
				Name:    "dst-1",
				Spec:    nc.DestinationSpec{VRFRef: ptr("vrf-1")},
				VRFSpec: &nc.VRFSpec{VRF: "v1", VNI: ptr(int32(1001))},
			},
		},
		RawDestinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "dst-1", Labels: map[string]string{"x": "y"}},
				Spec:       nc.DestinationSpec{VRFRef: ptr("vrf-1")},
			},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "l2a-1"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef:     "net-1",
					DisableAnycast: ptr(true),
					Destinations: &metav1.LabelSelector{
						MatchLabels: map[string]string{"x": "y"},
					},
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	l2 := result["node-1"].Layer2s["100"]
	if l2.IRB != nil {
		t.Error("expected nil IRB when anycast is disabled")
	}
}

func TestL2ABuilder_UnknownNetwork(t *testing.T) {
	b := NewL2ABuilder()

	data := &resolver.ResolvedData{
		Nodes:    []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		Networks: map[string]*resolver.ResolvedNetwork{},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-l2a"},
				Spec:       nc.Layer2AttachmentSpec{NetworkRef: "nonexistent"},
			},
		},
	}

	_, err := b.Build(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for unknown network reference")
	}
}

func TestL2ABuilder_DefaultMTU(t *testing.T) {
	b := NewL2ABuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-1": {
				Name: "net-1",
				Spec: nc.NetworkSpec{VLAN: ptr(int32(10)), VNI: ptr(int32(100))},
			},
		},
		Destinations: map[string]*resolver.ResolvedDestination{},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "l2a-1"},
				Spec:       nc.Layer2AttachmentSpec{NetworkRef: "net-1"},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	l2 := result["node-1"].Layer2s["10"]
	if l2.MTU != 1500 {
		t.Errorf("expected default MTU 1500, got %d", l2.MTU)
	}
}

func TestMatchNodes_NilSelector(t *testing.T) {
	nodes := []corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b"}},
	}

	matched, err := matchNodes(nodes, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 2 {
		t.Errorf("nil selector should match all nodes, got %d", len(matched))
	}
}

func TestMatchNodes_WithSelector(t *testing.T) {
	nodes := []corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "gpu-1", Labels: map[string]string{"gpu": "true"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "cpu-1", Labels: map[string]string{"gpu": "false"}}},
	}

	selector := &metav1.LabelSelector{
		MatchLabels: map[string]string{"gpu": "true"},
	}

	matched, err := matchNodes(nodes, selector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched node, got %d", len(matched))
	}
	if matched[0].Name != "gpu-1" {
		t.Errorf("expected gpu-1, got %s", matched[0].Name)
	}
}

func TestL2ABuilder_DuplicateNetworkOnSameNode(t *testing.T) {
	b := NewL2ABuilder()
	vlan := int32(501)
	vni := int32(10501)
	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-vlan501": {
				Name: "net-vlan501",
				Spec: nc.NetworkSpec{VLAN: &vlan, VNI: &vni, IPv4: &nc.IPNetwork{CIDR: "10.0.1.1/24"}},
			},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-first"}, Spec: nc.Layer2AttachmentSpec{NetworkRef: "net-vlan501"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-second"}, Spec: nc.Layer2AttachmentSpec{NetworkRef: "net-vlan501"}},
		},
	}

	_, err := b.Build(context.Background(), data)
	if err == nil {
		t.Fatal("expected error for duplicate L2As on same Network/node, got nil")
	}
	if !strings.Contains(err.Error(), "both target Network VLAN") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestL2ABuilder_SameNetworkDifferentNodes(t *testing.T) {
	b := NewL2ABuilder()
	vlan := int32(501)
	vni := int32(10501)
	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"zone": "a"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "node-2", Labels: map[string]string{"zone": "b"}}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-vlan501": {
				Name: "net-vlan501",
				Spec: nc.NetworkSpec{VLAN: &vlan, VNI: &vni, IPv4: &nc.IPNetwork{CIDR: "10.0.1.1/24"}},
			},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-zone-a"}, Spec: nc.Layer2AttachmentSpec{
				NetworkRef:   "net-vlan501",
				NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"zone": "a"}},
			}},
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-zone-b"}, Spec: nc.Layer2AttachmentSpec{
				NetworkRef:   "net-vlan501",
				NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"zone": "b"}},
			}},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("expected no error for same Network on different nodes, got: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 node contributions, got %d", len(result))
	}
}
