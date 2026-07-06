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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
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
		t.Errorf("expected IRB VRF 'prod' (backbone name), got %q", l2.IRB.VRF)
	}
	if len(l2.IRB.IPAddresses) != 1 || l2.IRB.IPAddresses[0] != "10.100.0.1/24" {
		t.Errorf("expected IRB IP [10.100.0.1/24], got %v", l2.IRB.IPAddresses)
	}

	// L2 VNI RouteTarget must be empty — FRR auto-derives it.
	// Setting it to the VRF's RT causes link-local type-2 EVPN routes (without RMAC)
	// to be imported into the VRF, corrupting nexthop router MACs.
	if l2.RouteTarget != "" {
		t.Errorf("expected empty L2 RouteTarget (FRR auto-derives), got %q", l2.RouteTarget)
	}

	// FabricVRF is keyed by backbone VRF name (spec.vrf), not CRD name.
	fvrf, ok := contrib.FabricVRFs["prod"]
	if !ok {
		t.Fatal("expected FabricVRF 'prod' (backbone name)")
	}
	if fvrf.VNI != 5001 {
		t.Errorf("expected FabricVRF VNI 5001, got %d", fvrf.VNI)
	}
}

func TestL2ABuilder_RejectsSharedL2L3RouteTarget(t *testing.T) {
	b := NewL2ABuilder()

	// Directly test buildLayer2 with a scenario where the RT would match the VRF's RT.
	// This exercises the hardening guard even though routeTarget() currently returns "".
	vrfSpec := &nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")}
	net := &resolver.ResolvedNetwork{
		Name: "test-net",
		Spec: nc.NetworkSpec{VLAN: ptr(int32(100)), VNI: ptr(int32(10100))},
	}
	l2a := &nc.Layer2Attachment{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-l2a"},
		Spec:       nc.Layer2AttachmentSpec{NetworkRef: "test-net"},
	}

	// routeTarget() returns "" so this should succeed (no collision).
	l2, err := b.buildLayer2(l2a, net, "", vrfSpec)
	if err != nil {
		t.Fatalf("expected no error for empty RT, got: %v", err)
	}
	if l2.RouteTarget != "" {
		t.Errorf("expected empty RT, got %q", l2.RouteTarget)
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

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unknown network should be skipped, not fatal: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty result when L2A references unknown network, got %d entries", len(result))
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

func TestL2ABuilder_DuplicateInterfaceNameOnSameNode(t *testing.T) {
	b := NewL2ABuilder()
	vlan501 := int32(501)
	vlan502 := int32(502)
	vni501 := int32(10501)
	vni502 := int32(10502)
	ifName := "bond0"
	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-vlan501": {Name: "net-vlan501", Spec: nc.NetworkSpec{VLAN: &vlan501, VNI: &vni501, IPv4: &nc.IPNetwork{CIDR: "10.0.1.1/24"}}},
			"net-vlan502": {Name: "net-vlan502", Spec: nc.NetworkSpec{VLAN: &vlan502, VNI: &vni502, IPv4: &nc.IPNetwork{CIDR: "10.0.2.1/24"}}},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-a"}, Spec: nc.Layer2AttachmentSpec{NetworkRef: "net-vlan501", InterfaceName: &ifName}},
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-b"}, Spec: nc.Layer2AttachmentSpec{NetworkRef: "net-vlan502", InterfaceName: &ifName}},
		},
	}

	// Isolation: the second L2A claiming an already-owned interface name is
	// skipped; the first L2A (l2a-a, VLAN 501) is still applied. The whole
	// builder must not fail.
	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("expected no error (conflicting L2A skipped), got: %v", err)
	}
	contrib, ok := result["node-1"]
	if !ok {
		t.Fatal("expected node-1 contribution from the first L2A")
	}
	if _, ok := contrib.Layer2s["501"]; !ok {
		t.Errorf("expected first L2A (VLAN 501) to be applied, keys=%v", keys(contrib.Layer2s))
	}
	if _, ok := contrib.Layer2s["502"]; ok {
		t.Error("expected conflicting L2A (VLAN 502) to be skipped")
	}
}

func TestL2ABuilder_NodeIPs_NotInIRB(t *testing.T) {
	b := NewL2ABuilder()
	vlan := int32(501)
	vni := int32(10501)
	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-vlan501": {Name: "net-vlan501", Spec: nc.NetworkSpec{
				VLAN: &vlan, VNI: &vni,
				IPv4: &nc.IPNetwork{CIDR: "10.0.1.1/24"},
			}},
		},
		RawDestinations: []nc.Destination{
			{ObjectMeta: metav1.ObjectMeta{Name: "dest-gw", Labels: map[string]string{"type": "gateway"}},
				Spec: nc.DestinationSpec{VRFRef: ptr("vrf-m2m")}},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dest-gw": {Name: "dest-gw", Spec: nc.DestinationSpec{VRFRef: ptr("vrf-m2m")}, VRFSpec: &nc.VRFSpec{VRF: "m2m", VNI: ptr(int32(100))}},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "l2a-nodeips"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef:   "net-vlan501",
					Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gateway"}},
					NodeIPs:      &nc.NodeIPConfig{Enabled: true},
				},
				Status: nc.Layer2AttachmentStatus{
					NodeAddresses: map[string]nc.AddressAllocation{
						"node-1": {IPv4: []string{"10.0.1.10"}},
					},
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Node IPs are host-side (NodeNetplanConfig), NOT on IRB.
	// IRB should only have the anycast gateway.
	n1Layer2 := result["node-1"].Layer2s["501"]
	if n1Layer2.IRB == nil {
		t.Fatal("node-1 Layer2 IRB is nil")
	}
	if len(n1Layer2.IRB.IPAddresses) != 1 {
		t.Errorf("IRB should only have anycast gateway (1 IP), got %d: %v", len(n1Layer2.IRB.IPAddresses), n1Layer2.IRB.IPAddresses)
	}
	if n1Layer2.IRB.IPAddresses[0] != "10.0.1.1/24" {
		t.Errorf("IRB IP should be anycast 10.0.1.1/24, got %s", n1Layer2.IRB.IPAddresses[0])
	}

	// NetplanNodeIPs should be populated with the per-node IP and gateway.
	nip, ok := result["node-1"].NetplanNodeIPs["501"]
	if !ok {
		t.Fatal("expected NetplanNodeIPs entry for key 501")
	}
	assert.Equal(t, []string{"10.0.1.10/24"}, nip.Addresses)
	assert.Equal(t, []string{"10.0.1.1"}, nip.Gateways)
}

func TestL2ABuilder_NodeIPs_DualStack(t *testing.T) {
	b := NewL2ABuilder()
	vlan := int32(501)
	vni := int32(10501)
	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-vlan501": {Name: "net-vlan501", Spec: nc.NetworkSpec{
				VLAN: &vlan, VNI: &vni,
				IPv4: &nc.IPNetwork{CIDR: "10.0.1.1/24"},
				IPv6: &nc.IPNetwork{CIDR: "2001:db8::1/64"},
			}},
		},
		RawDestinations: []nc.Destination{
			{ObjectMeta: metav1.ObjectMeta{Name: "dest-gw", Labels: map[string]string{"type": "gateway"}},
				Spec: nc.DestinationSpec{VRFRef: ptr("vrf-m2m")}},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dest-gw": {Name: "dest-gw", Spec: nc.DestinationSpec{VRFRef: ptr("vrf-m2m")}, VRFSpec: &nc.VRFSpec{VRF: "m2m", VNI: ptr(int32(100))}},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "l2a-nodeips"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef:   "net-vlan501",
					Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gateway"}},
					NodeIPs:      &nc.NodeIPConfig{Enabled: true},
				},
				Status: nc.Layer2AttachmentStatus{
					NodeAddresses: map[string]nc.AddressAllocation{
						"node-1": {IPv4: []string{"10.0.1.10"}, IPv6: []string{"2001:db8::10"}},
					},
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)

	nip := result["node-1"].NetplanNodeIPs["501"]
	assert.Equal(t, []string{"10.0.1.10/24", "2001:db8::10/64"}, nip.Addresses)
	assert.Equal(t, []string{"10.0.1.1", "2001:db8::1"}, nip.Gateways)
}

func TestL2ABuilder_NodeIPs_NotEnabled(t *testing.T) {
	b := NewL2ABuilder()
	vlan := int32(501)
	vni := int32(10501)
	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-vlan501": {Name: "net-vlan501", Spec: nc.NetworkSpec{
				VLAN: &vlan, VNI: &vni,
				IPv4: &nc.IPNetwork{CIDR: "10.0.1.1/24"},
			}},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "l2a-no-nodeips"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef: "net-vlan501",
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)
	assert.Empty(t, result["node-1"].NetplanNodeIPs, "NetplanNodeIPs should be empty when nodeIPs is not enabled")
}

func TestL2ABuilder_InterfaceNameAndRef(t *testing.T) {
	b := NewL2ABuilder()
	vlan := int32(501)
	vni := int32(10501)
	ifName := "chris-l2"
	ifRef := "bond0"
	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-vlan501": {Name: "net-vlan501", Spec: nc.NetworkSpec{VLAN: &vlan, VNI: &vni, IPv4: &nc.IPNetwork{CIDR: "10.0.1.1/24"}}},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "l2a-named"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef:    "net-vlan501",
					InterfaceName: &ifName,
					InterfaceRef:  &ifRef,
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)
	require.Contains(t, result, "node-1")

	contrib := result["node-1"]
	_, ok := contrib.Layer2s["501"]
	require.True(t, ok, "expected layer2 key 501")
	nip, ok := contrib.NetplanNodeIPs["501"]
	require.True(t, ok, "expected NetplanNodeIPs entry for key 501")
	assert.Equal(t, "chris-l2", nip.InterfaceName, "InterfaceName should be propagated to netplan device")
	assert.Equal(t, "bond0", nip.InterfaceRef, "InterfaceRef should be propagated to netplan device")
}

// TestL2ABuilder_IRBGateway_NetworkAddressCIDR reproduces the production bug
// where a Network authored with the network-address form of the CIDR (host bits
// zero) must yield the anycast gateway (network address + 1) in the IRB and in
// the per-node netplan default gateway, for both IPv4 and IPv6.
func TestL2ABuilder_IRBGateway_NetworkAddressCIDR(t *testing.T) {
	b := NewL2ABuilder()
	vlan := int32(149)
	vni := int32(100149)
	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-vlan149": {Name: "net-vlan149", Spec: nc.NetworkSpec{
				VLAN: &vlan, VNI: &vni,
				IPv4: &nc.IPNetwork{CIDR: "198.51.100.224/27"},
				IPv6: &nc.IPNetwork{CIDR: "2001:db8::/64"},
			}},
		},
		RawDestinations: []nc.Destination{
			{ObjectMeta: metav1.ObjectMeta{Name: "dest-gw", Labels: map[string]string{"type": "gateway"}},
				Spec: nc.DestinationSpec{VRFRef: ptr("vrf-ztn")}},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dest-gw": {Name: "dest-gw", Spec: nc.DestinationSpec{VRFRef: ptr("vrf-ztn")}, VRFSpec: &nc.VRFSpec{VRF: "ztn", VNI: ptr(int32(100))}},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "net-vlan149-l2a"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef:   "net-vlan149",
					Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gateway"}},
					NodeIPs:      &nc.NodeIPConfig{Enabled: true},
				},
				Status: nc.Layer2AttachmentStatus{
					NodeAddresses: map[string]nc.AddressAllocation{
						"node-1": {IPv4: []string{"198.51.100.230"}, IPv6: []string{"2001:db8::30"}},
					},
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)

	// IRB anycast gateways must be network address + 1, preserving prefix length.
	l2 := result["node-1"].Layer2s["149"]
	require.NotNil(t, l2.IRB, "expected IRB to be set")
	assert.Equal(t, []string{"198.51.100.225/27", "2001:db8::1/64"}, l2.IRB.IPAddresses)

	// The per-node netplan default gateways must also be network address + 1
	// (bare, without prefix), while the allocated host addresses are unchanged.
	nip := result["node-1"].NetplanNodeIPs["149"]
	assert.Equal(t, []string{"198.51.100.230/27", "2001:db8::30/64"}, nip.Addresses)
	assert.Equal(t, []string{"198.51.100.225", "2001:db8::1"}, nip.Gateways)
}

// TestL2ABuilder_IRBGateway_SingleHostReportsSkip verifies that a Network CIDR
// with no usable anycast gateway (a /32) does not abort the reconcile: the L2A
// is skipped, no IRB is emitted, and a Ready=False build issue with the
// specific "InvalidIRBGateway" reason is reported for the resource.
func TestL2ABuilder_IRBGateway_SingleHostReportsSkip(t *testing.T) {
	b := NewL2ABuilder()
	vlan := int32(151)
	vni := int32(100151)
	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"single-host": {Name: "single-host", Spec: nc.NetworkSpec{
				VLAN: &vlan, VNI: &vni,
				IPv4: &nc.IPNetwork{CIDR: "10.0.0.4/32"},
			}},
		},
		RawDestinations: []nc.Destination{
			{ObjectMeta: metav1.ObjectMeta{Name: "dest-gw", Labels: map[string]string{"type": "gateway"}},
				Spec: nc.DestinationSpec{VRFRef: ptr("vrf-sh")}},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"dest-gw": {Name: "dest-gw", Spec: nc.DestinationSpec{VRFRef: ptr("vrf-sh")}, VRFSpec: &nc.VRFSpec{VRF: "sh", VNI: ptr(int32(100))}},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "l2a-single-host"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef:   "single-host",
					Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gateway"}},
				},
			},
		},
	}

	report := NewBuildReport()
	ctx := WithReport(context.Background(), report)

	// Build must not error (reconcile is not aborted) even though the L2A is skipped.
	result, err := b.Build(ctx, data)
	require.NoError(t, err)

	// The L2A produced no Layer2 contribution for the node.
	if contrib, ok := result["node-1"]; ok {
		_, hasL2 := contrib.Layer2s["151"]
		assert.False(t, hasL2, "expected no Layer2 for a Network with no usable gateway")
	}

	// A build issue with the specific reason is reported for the L2A.
	issues := report.Issues()
	require.Len(t, issues, 1)
	assert.Equal(t, "Layer2Attachment", issues[0].Kind)
	assert.Equal(t, "l2a-single-host", issues[0].Name)
	assert.Equal(t, reasonInvalidIRBGateway, issues[0].Reason)
	assert.Contains(t, issues[0].Message, "no usable host address")
}

// TestGatewayAddr covers the gateway-derivation edge cases: normal subnets use
// the first usable host (network address + 1); point-to-point prefixes (/31,
// /127) use the network address itself; and single-host prefixes (/32, /128)
// have no usable gateway and return an error.
func TestGatewayAddr(t *testing.T) {
	tests := []struct {
		name     string
		cidr     string
		wantCIDR string // expected gatewayCIDR result; "" means expect error
		wantIP   string // expected parseCIDRParts gateway IP ("" on error)
		wantLen  string // expected parseCIDRParts prefix length ("" on error)
	}{
		{"ipv4 network address /24", "10.0.1.0/24", "10.0.1.1/24", "10.0.1.1", "24"},
		{"ipv4 network address /27", "198.51.100.224/27", "198.51.100.225/27", "198.51.100.225", "27"},
		{"ipv6 network address /64", "2001:db8::/64", "2001:db8::1/64", "2001:db8::1", "64"},
		{"ipv4 point-to-point /31", "10.0.0.0/31", "10.0.0.0/31", "10.0.0.0", "31"},
		{"ipv6 point-to-point /127", "2001:db8::/127", "2001:db8::/127", "2001:db8::", "127"},
		{"ipv4 single host /32", "10.0.0.5/32", "", "", ""},
		{"ipv6 single host /128", "2001:db8::5/128", "", "", ""},
		{"unparseable", "not-a-cidr", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gwCIDR, err := gatewayCIDR(tt.cidr)
			if tt.wantCIDR == "" {
				assert.Error(t, err, "expected error for %q", tt.cidr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantCIDR, gwCIDR)
			}

			ip, plen := parseCIDRParts(tt.cidr)
			assert.Equal(t, tt.wantIP, ip)
			assert.Equal(t, tt.wantLen, plen)
		})
	}
}
