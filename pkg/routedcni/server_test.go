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

package routedcni

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/routedcni/pb"
)

// testPortIface is the CRA-side interface/tap name used across the Add/Del tests.
const testPortIface = "port-cid-1"

func newFakeClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func getNRP(t *testing.T, c client.Client, node string) *v1alpha1.NodeRoutedPorts {
	t.Helper()
	nrp := &v1alpha1.NodeRoutedPorts{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: node}, nrp); err != nil {
		t.Fatalf("getting NodeRoutedPorts: %v", err)
	}
	return nrp
}

func TestServerAddCreatesAndUpserts(t *testing.T) {
	c := newFakeClient(t)
	s := NewServer(c, "node-1", logr.Discard())
	ctx := context.Background()

	addReq := &pb.AddRequest{
		PodNamespace: "ns",
		PodName:      "vm-launcher",
		ContainerId:  "cid-1",
		Vrf:          "",
		Port: &pb.RoutedPort{
			Interface:  testPortIface,
			GatewayV4:  "169.254.1.1/32",
			GatewayV6:  "fe80::1/128",
			HostRoutes: []string{"10.201.0.10/32", "fd00:201::10/128"},
		},
	}
	resp, err := s.Add(ctx, addReq)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if resp.GetTapName() != testPortIface {
		t.Fatalf("Add returned TapName %q, want %q (the grout flavor waits for this tap)", resp.GetTapName(), testPortIface)
	}

	nrp := getNRP(t, c, "node-1")
	if len(nrp.Spec.Ports) != 1 {
		t.Fatalf("expected 1 port after add, got %d", len(nrp.Spec.Ports))
	}
	if nrp.Spec.Ports[0].Interface != testPortIface {
		t.Fatalf("unexpected interface %q", nrp.Spec.Ports[0].Interface)
	}

	// Repeating the same Add upserts (no duplicate).
	if _, err := s.Add(ctx, addReq); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	nrp = getNRP(t, c, "node-1")
	if len(nrp.Spec.Ports) != 1 {
		t.Fatalf("expected Add to be idempotent, got %d ports", len(nrp.Spec.Ports))
	}
}

func TestServerDelRemoves(t *testing.T) {
	c := newFakeClient(t)
	s := NewServer(c, "node-1", logr.Discard())
	ctx := context.Background()

	if _, err := s.Add(ctx, &pb.AddRequest{
		ContainerId: "cid-1",
		Port:        &pb.RoutedPort{Interface: testPortIface},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := s.Del(ctx, &pb.DelRequest{ContainerId: "cid-1", Interface: testPortIface}); err != nil {
		t.Fatalf("Del: %v", err)
	}
	nrp := getNRP(t, c, "node-1")
	if len(nrp.Spec.Ports) != 0 {
		t.Fatalf("expected 0 ports after del, got %d", len(nrp.Spec.Ports))
	}

	// Deleting an unknown attachment succeeds (idempotent), and does not create
	// an object if none exists.
	c2 := newFakeClient(t)
	s2 := NewServer(c2, "node-2", logr.Discard())
	if _, err := s2.Del(ctx, &pb.DelRequest{ContainerId: "missing"}); err != nil {
		t.Fatalf("Del on missing object: %v", err)
	}
	nrp2 := &v1alpha1.NodeRoutedPorts{}
	if err := c2.Get(ctx, types.NamespacedName{Name: "node-2"}, nrp2); err == nil {
		t.Fatal("expected no NodeRoutedPorts object to be created by a no-op Del")
	}
}

func TestServerAddValidatesInput(t *testing.T) {
	s := NewServer(newFakeClient(t), "node-1", logr.Discard())
	ctx := context.Background()

	if _, err := s.Add(ctx, &pb.AddRequest{ContainerId: "cid-1"}); err == nil {
		t.Fatal("expected error when port is missing")
	}
	if _, err := s.Add(ctx, &pb.AddRequest{Port: &pb.RoutedPort{Interface: "x"}}); err == nil {
		t.Fatal("expected error when container_id is missing")
	}
}

func TestNodeSourceReadsEntries(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()

	// No object yet -> nil, no error.
	src := NewNodeSource(c, "node-1")
	entries, err := src.RoutedPorts(ctx)
	if err != nil || entries != nil {
		t.Fatalf("expected nil entries and no error, got %v / %v", entries, err)
	}

	s := NewServer(c, "node-1", logr.Discard())
	if _, err := s.Add(ctx, &pb.AddRequest{
		ContainerId: "cid-1",
		Port:        &pb.RoutedPort{Interface: testPortIface},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err = src.RoutedPorts(ctx)
	if err != nil {
		t.Fatalf("RoutedPorts: %v", err)
	}
	if len(entries) != 1 || entries[0].Interface != testPortIface {
		t.Fatalf("unexpected entries %+v", entries)
	}
}

// TestPortTransportFromPB locks the wire-transport -> API-enum mapping the CRA
// renderers depend on. In particular the grout flavor's "grouttap" transport
// must map to the veth enum: for an L2 attach the grout renderer emits the port
// as a net_tap enslaved to the L2VNI bridge (AddTapPortToBridge), exactly like a
// veth attach. Only "vhostuser" maps to the vhost-user enum.
func TestPortTransportFromPB(t *testing.T) {
	cases := []struct {
		in   string
		want v1alpha1.PortTransport
	}{
		{"grouttap", v1alpha1.PortTransportVeth},
		{"veth", v1alpha1.PortTransportVeth},
		{"", v1alpha1.PortTransportVeth},
		{"vhostuser", v1alpha1.PortTransportVhostUser},
	}
	for _, tc := range cases {
		if got := portTransportFromPB(tc.in); got != tc.want {
			t.Errorf("portTransportFromPB(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
