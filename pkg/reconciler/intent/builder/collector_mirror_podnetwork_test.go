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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// ---------------------------------------------------------------------------
// CollectorBuilder tests
// ---------------------------------------------------------------------------

func TestCollectorBuilder_Name(t *testing.T) {
	b := NewCollectorBuilder()
	if b.Name() != "collector" {
		t.Errorf("expected name 'collector', got %q", b.Name())
	}
}

func TestCollectorBuilder_EmptyData(t *testing.T) {
	b := NewCollectorBuilder()
	result, err := b.Build(context.Background(), &resolver.ResolvedData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestCollectorBuilder_BasicCollector(t *testing.T) {
	b := NewCollectorBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}},
		},
		VRFs: map[string]*resolver.ResolvedVRF{
			"mirror-vrf": {
				Name: "mirror-vrf",
				Spec: nc.VRFSpec{VRF: "mirror", VNI: ptr(int32(9000)), RouteTarget: ptr("65000:9000")},
			},
		},
		Collectors: []nc.Collector{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "my-collector"},
				Spec: nc.CollectorSpec{
					Address:  "10.0.0.99",
					Protocol: "l3gre",
					MirrorVRF: nc.MirrorVRFRef{
						Name: "mirror-vrf",
						Loopback: nc.LoopbackConfig{
							Name:   "lo.mir",
							Subnet: "10.250.0.0/24",
						},
					},
				},
				Status: nc.CollectorStatus{
					NodeAddresses: map[string]string{
						"node-1": "10.250.0.1",
						"node-2": "10.250.0.2",
					},
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

		fvrf, ok := contrib.FabricVRFs["mirror"]
		if !ok {
			t.Fatalf("expected FabricVRF 'mirror' for %s, got keys %v", nodeName, keys(contrib.FabricVRFs))
		}

		if fvrf.VNI != 9000 {
			t.Errorf("expected VNI 9000, got %d", fvrf.VNI)
		}

		lb, ok := fvrf.Loopbacks["lo.mir"]
		if !ok {
			t.Fatalf("expected loopback 'lo.mir' for %s", nodeName)
		}
		// The loopback IP must be the per-node allocation from
		// status.nodeAddresses, NOT spec.address (which is the remote GRE peer).
		expected := map[string]string{"node-1": "10.250.0.1", "node-2": "10.250.0.2"}[nodeName]
		if len(lb.IPAddresses) != 1 || lb.IPAddresses[0] != expected {
			t.Errorf("node %s: expected loopback IP [%s], got %v", nodeName, expected, lb.IPAddresses)
		}
	}
}

func TestCollectorBuilder_UnknownVRF(t *testing.T) {
	b := NewCollectorBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		VRFs: map[string]*resolver.ResolvedVRF{},
		Collectors: []nc.Collector{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-collector"},
				Spec: nc.CollectorSpec{
					Address:  "10.0.0.1",
					Protocol: "l3gre",
					MirrorVRF: nc.MirrorVRFRef{
						Name:     "nonexistent-vrf",
						Loopback: nc.LoopbackConfig{Name: "lo.mir"},
					},
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("expected no error (offending collector skipped), got: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected no contributions for skipped collector, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// MirrorBuilder tests
// ---------------------------------------------------------------------------

func TestMirrorBuilder_Name(t *testing.T) {
	b := NewMirrorBuilder()
	if b.Name() != "mirror" {
		t.Errorf("expected name 'mirror', got %q", b.Name())
	}
}

func TestMirrorBuilder_EmptyData(t *testing.T) {
	b := NewMirrorBuilder()
	result, err := b.Build(context.Background(), &resolver.ResolvedData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestMirrorBuilder_Layer2Source(t *testing.T) {
	b := NewMirrorBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"storage-net": {
				Name: "storage-net",
				Spec: nc.NetworkSpec{
					VLAN: ptr(int32(200)),
					VNI:  ptr(int32(10200)),
				},
			},
		},
		Collectors: []nc.Collector{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "col-1"},
				Spec: nc.CollectorSpec{
					Address:  "10.0.0.99",
					Protocol: "l3gre",
					MirrorVRF: nc.MirrorVRFRef{
						Name:     "mirror-vrf",
						Loopback: nc.LoopbackConfig{Name: "lo.mir"},
					},
				},
			},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "storage-l2a"},
				Spec: nc.Layer2AttachmentSpec{
					NetworkRef: "storage-net",
				},
			},
		},
		TrafficMirrors: []nc.TrafficMirror{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "tm-1"},
				Spec: nc.TrafficMirrorSpec{
					Source: nc.MirrorSource{
						Kind: "Layer2Attachment",
						Name: "storage-l2a",
					},
					Collector: "col-1",
					Direction: "ingress",
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
	if contrib == nil {
		t.Fatal("expected contribution for node-1")
	}

	// Layer2 entry keyed by VLAN.
	l2, ok := contrib.Layer2s["200"]
	if !ok {
		t.Fatalf("expected Layer2 with key '200', got keys %v", keys(contrib.Layer2s))
	}

	if len(l2.MirrorACLs) != 1 {
		t.Fatalf("expected 1 MirrorACL, got %d", len(l2.MirrorACLs))
	}

	acl := l2.MirrorACLs[0]
	wantGRE := mirrorGREName(&nc.Collector{
		ObjectMeta: metav1.ObjectMeta{Name: "col-1"},
		Spec:       nc.CollectorSpec{Protocol: "l3gre"},
	})
	if acl.MirrorDestination != wantGRE {
		t.Errorf("expected mirror destination %q, got %q", wantGRE, acl.MirrorDestination)
	}
	if acl.Direction != networkv1alpha1.MirrorDirectionIngress {
		t.Errorf("expected direction ingress, got %q", acl.Direction)
	}
}

func TestMirrorBuilder_PureL2SourceRejected(t *testing.T) {
	b := NewMirrorBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		Networks: map[string]*resolver.ResolvedNetwork{
			// Pure L2: no VNI. Mirroring is a CRA/HBN feature.
			"pure-l2": {Name: "pure-l2", Spec: nc.NetworkSpec{VLAN: ptr(int32(700))}},
		},
		Collectors: []nc.Collector{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "col-1"},
				Spec: nc.CollectorSpec{
					Address:   "10.0.0.99",
					Protocol:  "l3gre",
					MirrorVRF: nc.MirrorVRFRef{Name: "mirror-vrf", Loopback: nc.LoopbackConfig{Name: "lo.mir"}},
				},
			},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "pl2-l2a"},
				Spec:       nc.Layer2AttachmentSpec{NetworkRef: "pure-l2", InterfaceRef: ptr("eth1")},
			},
		},
		TrafficMirrors: []nc.TrafficMirror{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "tm-1"},
				Spec: nc.TrafficMirrorSpec{
					Source:    nc.MirrorSource{Kind: "Layer2Attachment", Name: "pl2-l2a"},
					Collector: "col-1",
					Direction: "ingress",
				},
			},
		},
	}

	report := NewBuildReport()
	result, err := b.Build(WithReport(context.Background(), report), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No bare Layer2 entry (VLAN/VNI/MTU=0) must be produced; the mirror is
	// skipped with a Ready=False condition instead.
	if contrib := result["node-1"]; contrib != nil && len(contrib.Layer2s) != 0 {
		t.Errorf("expected no Layer2 entry for a pure-L2 mirror source, got keys %v", keys(contrib.Layer2s))
	}
	issues := report.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Kind != "TrafficMirror" {
		t.Errorf("expected TrafficMirror issue, got %q", issues[0].Kind)
	}
}

func TestMirrorBuilder_InboundSource(t *testing.T) {
	b := NewMirrorBuilder()

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
		Destinations: map[string]*resolver.ResolvedDestination{
			"corp-dc": {
				Name:    "corp-dc",
				Spec:    nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
				VRFSpec: &nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")},
			},
		},
		RawDestinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "corp-dc", Labels: map[string]string{"env": "prod"}},
				Spec:       nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
			},
		},
		Collectors: []nc.Collector{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "col-1"},
				Spec: nc.CollectorSpec{
					Address:  "10.0.0.50",
					Protocol: "l3gre",
					MirrorVRF: nc.MirrorVRFRef{
						Name:     "mirror-vrf",
						Loopback: nc.LoopbackConfig{Name: "lo.mir"},
					},
				},
			},
		},
		Inbounds: []nc.Inbound{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "my-inbound"},
				Spec: nc.InboundSpec{
					NetworkRef:    "svc-net",
					Destinations:  &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
					Advertisement: nc.AdvertisementConfig{},
				},
			},
		},
		TrafficMirrors: []nc.TrafficMirror{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "tm-inbound"},
				Spec: nc.TrafficMirrorSpec{
					Source: nc.MirrorSource{
						Kind: "Inbound",
						Name: "my-inbound",
					},
					Collector: "col-1",
					Direction: "both",
					TrafficMatch: &nc.TrafficMatch{
						Protocol:  ptr("TCP"),
						SrcPrefix: ptr("10.0.0.0/8"),
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

	fvrf, ok := contrib.FabricVRFs["prod"]
	if !ok {
		t.Fatalf("expected FabricVRF 'prod', got keys %v", keys(contrib.FabricVRFs))
	}

	if len(fvrf.MirrorACLs) != 2 {
		t.Fatalf("expected 2 MirrorACLs on FabricVRF (both directions), got %d", len(fvrf.MirrorACLs))
	}

	wantGRE := mirrorGREName(&nc.Collector{
		ObjectMeta: metav1.ObjectMeta{Name: "col-1"},
		Spec:       nc.CollectorSpec{Protocol: "l3gre"},
	})
	dirs := map[networkv1alpha1.MirrorDirection]bool{}
	for i := range fvrf.MirrorACLs {
		acl := fvrf.MirrorACLs[i]
		if acl.MirrorDestination != wantGRE {
			t.Errorf("expected mirror destination %q, got %q", wantGRE, acl.MirrorDestination)
		}
		dirs[acl.Direction] = true

		// Verify traffic match was converted.
		if acl.TrafficMatch.Protocol == nil || *acl.TrafficMatch.Protocol != "TCP" {
			t.Errorf("expected protocol TCP, got %v", acl.TrafficMatch.Protocol)
		}
		if acl.TrafficMatch.SrcPrefix == nil || *acl.TrafficMatch.SrcPrefix != "10.0.0.0/8" {
			t.Errorf("expected src prefix '10.0.0.0/8', got %v", acl.TrafficMatch.SrcPrefix)
		}
	}
	if !dirs[networkv1alpha1.MirrorDirectionIngress] || !dirs[networkv1alpha1.MirrorDirectionEgress] {
		t.Errorf("expected both ingress and egress ACLs, got directions %v", dirs)
	}
}

func TestMirrorBuilder_InboundSourceFanOut(t *testing.T) {
	// C2: when an Inbound's Destinations selector matches multiple Destinations
	// across multiple VRFs, the MirrorACL must land on every matched VRF.
	b := NewMirrorBuilder()

	prodSpec := &nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001)), RouteTarget: ptr("65000:5001")}
	dmzSpec := &nc.VRFSpec{VRF: "dmz", VNI: ptr(int32(5002)), RouteTarget: ptr("65000:5002")}

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		VRFs: map[string]*resolver.ResolvedVRF{
			"prod-vrf": {Name: "prod-vrf", Spec: *prodSpec},
			"dmz-vrf":  {Name: "dmz-vrf", Spec: *dmzSpec},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"prod-dest": {Name: "prod-dest", Spec: nc.DestinationSpec{VRFRef: ptr("prod-vrf")}, VRFSpec: prodSpec},
			"dmz-dest":  {Name: "dmz-dest", Spec: nc.DestinationSpec{VRFRef: ptr("dmz-vrf")}, VRFSpec: dmzSpec},
		},
		RawDestinations: []nc.Destination{
			{ObjectMeta: metav1.ObjectMeta{Name: "prod-dest", Labels: map[string]string{"tier": "shared"}},
				Spec: nc.DestinationSpec{VRFRef: ptr("prod-vrf")}},
			{ObjectMeta: metav1.ObjectMeta{Name: "dmz-dest", Labels: map[string]string{"tier": "shared"}},
				Spec: nc.DestinationSpec{VRFRef: ptr("dmz-vrf")}},
		},
		Collectors: []nc.Collector{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "col-1"},
				Spec: nc.CollectorSpec{
					Address:   "10.0.0.50",
					Protocol:  "l3gre",
					MirrorVRF: nc.MirrorVRFRef{Name: "mirror-vrf", Loopback: nc.LoopbackConfig{Name: "lo.mir"}},
				},
			},
		},
		Inbounds: []nc.Inbound{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "shared-inbound"},
				Spec: nc.InboundSpec{
					NetworkRef:   "svc-net",
					Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "shared"}},
				},
			},
		},
		TrafficMirrors: []nc.TrafficMirror{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "tm"},
				Spec: nc.TrafficMirrorSpec{
					Source:    nc.MirrorSource{Kind: "Inbound", Name: "shared-inbound"},
					Collector: "col-1",
					Direction: "both",
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
		t.Fatal("expected contribution")
	}
	for _, vrf := range []string{"prod", "dmz"} {
		fvrf, ok := contrib.FabricVRFs[vrf]
		if !ok {
			t.Fatalf("expected MirrorACL on FabricVRF %q, keys=%v", vrf, keys(contrib.FabricVRFs))
		}
		if len(fvrf.MirrorACLs) != 2 {
			t.Errorf("VRF %q: expected 2 MirrorACLs (both directions), got %d", vrf, len(fvrf.MirrorACLs))
		}
	}
}

func TestMirrorBuilder_UnknownCollector(t *testing.T) {
	b := NewMirrorBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		TrafficMirrors: []nc.TrafficMirror{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "tm-bad"},
				Spec: nc.TrafficMirrorSpec{
					Source: nc.MirrorSource{
						Kind: "Layer2Attachment",
						Name: "some-l2a",
					},
					Collector: "nonexistent-collector",
					Direction: "ingress",
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("expected no error (offending TrafficMirror skipped), got: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected no contributions for skipped TrafficMirror, got %d", len(result))
	}
}

func TestMirrorBuilder_UnknownSourceKind(t *testing.T) {
	b := NewMirrorBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Collectors: []nc.Collector{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "col-1"},
				Spec: nc.CollectorSpec{
					Address:  "10.0.0.1",
					Protocol: "l3gre",
					MirrorVRF: nc.MirrorVRFRef{
						Name:     "mirror-vrf",
						Loopback: nc.LoopbackConfig{Name: "lo.mir"},
					},
				},
			},
		},
		TrafficMirrors: []nc.TrafficMirror{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "tm-bad-kind"},
				Spec: nc.TrafficMirrorSpec{
					Source: nc.MirrorSource{
						Kind: "UnknownKind",
						Name: "whatever",
					},
					Collector: "col-1",
					Direction: "ingress",
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("expected no error (offending TrafficMirror skipped), got: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected no contributions for skipped TrafficMirror, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// PodNetworkBuilder tests
// ---------------------------------------------------------------------------

func TestPodNetworkBuilder_Name(t *testing.T) {
	b := NewPodNetworkBuilder()
	if b.Name() != "podnetwork" {
		t.Errorf("expected name 'podnetwork', got %q", b.Name())
	}
}

func TestPodNetworkBuilder_EmptyData(t *testing.T) {
	b := NewPodNetworkBuilder()
	result, err := b.Build(context.Background(), &resolver.ResolvedData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions, got %d", len(result))
	}
}

func TestPodNetworkBuilder_BasicPodNetwork(t *testing.T) {
	b := NewPodNetworkBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"pod-net": {
				Name: "pod-net",
				Spec: nc.NetworkSpec{
					IPv4: &nc.IPNetwork{CIDR: "10.244.0.0/16"},
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
		PodNetworks: []nc.PodNetwork{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "default-pn"},
				Spec: nc.PodNetworkSpec{
					NetworkRef: "pod-net",
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

	if len(result) != 1 {
		t.Fatalf("expected 1 node contribution, got %d", len(result))
	}

	contrib := result["node-1"]
	if contrib == nil {
		t.Fatal("expected contribution for node-1")
	}

	fvrf, ok := contrib.FabricVRFs["prod"]
	if !ok {
		t.Fatalf("expected FabricVRF 'prod', got keys %v", keys(contrib.FabricVRFs))
	}

	if fvrf.VNI != 5001 {
		t.Errorf("expected VNI 5001, got %d", fvrf.VNI)
	}

	// Redistribute connected should have a filter for the pod CIDR.
	if fvrf.Redistribute == nil {
		t.Fatal("expected Redistribute to be set")
	}
	if fvrf.Redistribute.Connected == nil {
		t.Fatal("expected Redistribute.Connected to be set")
	}
	if len(fvrf.Redistribute.Connected.Items) != 1 {
		t.Fatalf("expected 1 redistribute filter item, got %d", len(fvrf.Redistribute.Connected.Items))
	}

	item := fvrf.Redistribute.Connected.Items[0]
	if item.Matcher.Prefix == nil || item.Matcher.Prefix.Prefix != "10.244.0.0/16" {
		t.Errorf("expected redistribute prefix '10.244.0.0/16', got %+v", item.Matcher.Prefix)
	}
	if item.Action.Type != networkv1alpha1.Accept {
		t.Errorf("expected Accept action, got %q", item.Action.Type)
	}
	if fvrf.Redistribute.Connected.DefaultAction.Type != networkv1alpha1.Reject {
		t.Errorf("expected Reject default action, got %q", fvrf.Redistribute.Connected.DefaultAction.Type)
	}
}

func TestPodNetworkBuilder_AggregateRoute(t *testing.T) {
	b := NewPodNetworkBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"pod-net": {
				Name: "pod-net",
				Spec: nc.NetworkSpec{
					IPv4: &nc.IPNetwork{CIDR: "10.244.0.0/16"},
				},
			},
		},
		VRFs: map[string]*resolver.ResolvedVRF{
			"prod-vrf": {
				Name: "prod-vrf",
				Spec: nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001))},
			},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"corp-dc": {
				Name:    "corp-dc",
				Spec:    nc.DestinationSpec{VRFRef: ptr("prod-vrf")},
				VRFSpec: &nc.VRFSpec{VRF: "prod", VNI: ptr(int32(5001))},
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
		PodNetworks: []nc.PodNetwork{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "pn-basic"},
				Spec: nc.PodNetworkSpec{
					NetworkRef: "pod-net",
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
	fvrf := contrib.FabricVRFs["prod"]

	if len(fvrf.StaticRoutes) != 1 {
		t.Fatalf("expected 1 aggregate static route, got %d", len(fvrf.StaticRoutes))
	}
	if fvrf.StaticRoutes[0].Prefix != "10.244.0.0/16" {
		t.Errorf("expected aggregate 10.244.0.0/16, got %s", fvrf.StaticRoutes[0].Prefix)
	}
}

func TestPodNetworkBuilder_UnknownNetwork(t *testing.T) {
	b := NewPodNetworkBuilder()

	data := &resolver.ResolvedData{
		Nodes:    []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		Networks: map[string]*resolver.ResolvedNetwork{},
		PodNetworks: []nc.PodNetwork{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-pn"},
				Spec: nc.PodNetworkSpec{
					NetworkRef: "nonexistent-net",
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("expected no error (offending PodNetwork skipped), got: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected no contributions for skipped PodNetwork, got %d", len(result))
	}
}

func TestPodNetworkBuilder_NoDestinations(t *testing.T) {
	b := NewPodNetworkBuilder()

	data := &resolver.ResolvedData{
		Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		Networks: map[string]*resolver.ResolvedNetwork{
			"pod-net": {
				Name: "pod-net",
				Spec: nc.NetworkSpec{
					IPv4: &nc.IPNetwork{CIDR: "10.244.0.0/16"},
				},
			},
		},
		PodNetworks: []nc.PodNetwork{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "pn-no-dest"},
				Spec: nc.PodNetworkSpec{
					NetworkRef: "pod-net",
					// No Destinations selector → no VRF → skipped.
				},
			},
		},
	}

	result, err := b.Build(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 contributions when no destinations, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// keys returns the map keys as a slice (for error messages).
func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
