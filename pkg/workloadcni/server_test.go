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

package workloadcni

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni/pb"
)

func newFakeClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func getNRP(t *testing.T, c client.Client, node string) *v1alpha1.NodeWorkloadPorts {
	t.Helper()
	nrp := &v1alpha1.NodeWorkloadPorts{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: node}, nrp); err != nil {
		t.Fatalf("getting NodeWorkloadPorts: %v", err)
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
		Port: &pb.WorkloadPort{
			Interface:  "cra0cid1",
			GatewayV4:  "169.254.1.1/32",
			GatewayV6:  "fe80::1/128",
			HostRoutes: []string{"10.201.0.10/32", "fd00:201::10/128"},
		},
	}
	if _, err := s.Add(ctx, addReq); err != nil {
		t.Fatalf("Add: %v", err)
	}

	nrp := getNRP(t, c, "node-1")
	if len(nrp.Spec.Ports) != 1 {
		t.Fatalf("expected 1 port after add, got %d", len(nrp.Spec.Ports))
	}
	if nrp.Spec.Ports[0].Interface != "cra0cid1" {
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
		PodNamespace: "ns",
		PodName:      "vm-launcher",
		ContainerId:  "cid-1",
		Port:         &pb.WorkloadPort{Interface: "cra0cid1"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := s.Del(ctx, &pb.DelRequest{ContainerId: "cid-1", Interface: "cra0cid1"}); err != nil {
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
	nrp2 := &v1alpha1.NodeWorkloadPorts{}
	if err := c2.Get(ctx, types.NamespacedName{Name: "node-2"}, nrp2); err == nil {
		t.Fatal("expected no NodeWorkloadPorts object to be created by a no-op Del")
	}
}

const (
	testTransportVhostUser = "vhostuser"
	testVhostSocketPath    = "/run/vsr-vhost-user/abc/socket"
	testSocketModeServer   = "server"
)

// clearRouted strips the routed-mode fields from a request so it can carry an
// L2 attachment, which is mutually exclusive with them.
func clearRouted(r *pb.AddRequest) {
	r.Vrf = ""
	r.Port.GatewayV4 = ""
	r.Port.GatewayV6 = ""
	r.Port.HostRoutes = nil
}

func TestServerAddValidatesInput(t *testing.T) {
	s := NewServer(newFakeClient(t), "node-1", logr.Discard())
	ctx := context.Background()

	valid := func() *pb.AddRequest {
		return &pb.AddRequest{
			PodNamespace: "ns",
			PodName:      "vm-launcher",
			ContainerId:  "cid-1",
			Port: &pb.WorkloadPort{
				Interface:  "cra012345",
				GatewayV4:  "169.254.1.1/32",
				HostRoutes: []string{"10.201.0.10/32"},
			},
		}
	}

	tests := map[string]func(*pb.AddRequest){
		"port missing":         func(r *pb.AddRequest) { r.Port = nil },
		"interface missing":    func(r *pb.AddRequest) { r.Port.Interface = "" },
		"interface too long":   func(r *pb.AddRequest) { r.Port.Interface = "cra0123456789abcdef" },
		"container_id missing": func(r *pb.AddRequest) { r.ContainerId = "" },
		"namespace missing":    func(r *pb.AddRequest) { r.PodNamespace = "" },
		"pod name missing":     func(r *pb.AddRequest) { r.PodName = "" },
		"bad gateway v4":       func(r *pb.AddRequest) { r.Port.GatewayV4 = "169.254.1.1" },
		"bad gateway v6":       func(r *pb.AddRequest) { r.Port.GatewayV6 = "not-an-address" },
		"v6 in gateway v4":     func(r *pb.AddRequest) { r.Port.GatewayV4 = "fe80::1/128" },
		"v4 in gateway v6":     func(r *pb.AddRequest) { r.Port.GatewayV6 = "169.254.1.1/32" },
		"bad host route":       func(r *pb.AddRequest) { r.Port.HostRoutes = []string{"10.201.0.10"} },
		"subnet host route":    func(r *pb.AddRequest) { r.Port.HostRoutes = []string{"10.201.0.0/24"} },
		"v6 subnet host route": func(r *pb.AddRequest) { r.Port.HostRoutes = []string{"fd00:201::/64"} },
		"subnet gateway v4":    func(r *pb.AddRequest) { r.Port.GatewayV4 = "169.254.1.1/24" },
		"subnet gateway v6":    func(r *pb.AddRequest) { r.Port.GatewayV6 = "fe80::1/64" },
		"vhostuser interface too long": func(r *pb.AddRequest) {
			// fpvhost-<ifname> is two characters longer than infra-<ifname>, so a
			// name that is fine for veth overflows the kernel limit here.
			r.Port.Interface = "cra012345"
			r.Port.Transport = testTransportVhostUser
			r.Port.SocketPath = testVhostSocketPath
			r.Port.SocketMode = testSocketModeServer
		},
		"unknown transport": func(r *pb.AddRequest) {
			r.Port.Transport = "sriov"
		},
		"socket path on veth": func(r *pb.AddRequest) {
			r.Port.SocketPath = testVhostSocketPath
		},
		"socket mode on veth": func(r *pb.AddRequest) {
			r.Port.SocketMode = testSocketModeServer
		},
		"vhostuser without socket path": func(r *pb.AddRequest) {
			r.Port.Transport = testTransportVhostUser
			r.Port.SocketMode = testSocketModeServer
		},
		"vhostuser without socket mode": func(r *pb.AddRequest) {
			r.Port.Transport = testTransportVhostUser
			r.Port.SocketPath = testVhostSocketPath
		},
		"vhostuser bad socket mode": func(r *pb.AddRequest) {
			r.Port.Transport = testTransportVhostUser
			r.Port.SocketPath = testVhostSocketPath
			r.Port.SocketMode = "bogus"
		},
		"l2 ref without name": func(r *pb.AddRequest) {
			clearRouted(r)
			r.Layer2AttachmentRef = &pb.Layer2AttachmentRef{}
		},
		"l2 ref with routed fields": func(r *pb.AddRequest) {
			r.Layer2AttachmentRef = &pb.Layer2AttachmentRef{Name: "blue"}
		},
		"l2 ref and trunk together": func(r *pb.AddRequest) {
			clearRouted(r)
			r.Layer2AttachmentRef = &pb.Layer2AttachmentRef{Name: "blue"}
			r.Layer2Trunk = []*pb.Layer2TrunkMember{
				{Ref: &pb.Layer2AttachmentRef{Name: "green"}},
			}
		},
		"trunk member without name": func(r *pb.AddRequest) {
			clearRouted(r)
			r.Layer2Trunk = []*pb.Layer2TrunkMember{{Ref: &pb.Layer2AttachmentRef{}}}
		},
		"trunk member without ref": func(r *pb.AddRequest) {
			clearRouted(r)
			r.Layer2Trunk = []*pb.Layer2TrunkMember{{Vlan: 100}}
		},
		"trunk vlan out of range": func(r *pb.AddRequest) {
			clearRouted(r)
			r.Layer2Trunk = []*pb.Layer2TrunkMember{
				{Ref: &pb.Layer2AttachmentRef{Name: "green"}, Vlan: 4095},
			}
		},
		"trunk duplicate ref": func(r *pb.AddRequest) {
			clearRouted(r)
			r.Layer2Trunk = []*pb.Layer2TrunkMember{
				{Ref: &pb.Layer2AttachmentRef{Name: "green"}},
				{Ref: &pb.Layer2AttachmentRef{Name: "green"}, Vlan: 100},
			}
		},
		"trunk duplicate vlan": func(r *pb.AddRequest) {
			clearRouted(r)
			r.Layer2Trunk = []*pb.Layer2TrunkMember{
				{Ref: &pb.Layer2AttachmentRef{Name: "green"}, Vlan: 100},
				{Ref: &pb.Layer2AttachmentRef{Name: "red"}, Vlan: 100},
			}
		},
		"trunk with routed fields": func(r *pb.AddRequest) {
			r.Layer2Trunk = []*pb.Layer2TrunkMember{
				{Ref: &pb.Layer2AttachmentRef{Name: "green"}},
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			req := valid()
			mutate(req)
			_, err := s.Add(ctx, req)
			if err == nil {
				t.Fatal("expected an error")
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
			}
		})
	}

	// A well-formed vhost-user request is accepted.
	vhost := valid()
	vhost.ContainerId = "cid-vhost"
	vhost.Port.Interface = "v012345"
	vhost.Port.Transport = testTransportVhostUser
	vhost.Port.SocketPath = testVhostSocketPath
	vhost.Port.SocketMode = testSocketModeServer
	if _, err := s.Add(ctx, vhost); err != nil {
		t.Fatalf("valid vhost-user request rejected: %v", err)
	}

	// A well-formed dual-stack request is accepted.
	req := valid()
	req.Port.GatewayV6 = "fe80::1/128"
	req.Port.HostRoutes = []string{"10.201.0.10/32", "fd00:201::10/128"}
	if _, err := s.Add(ctx, req); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

func TestNodeSourceReadsEntries(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()

	// No object yet -> nil, no error.
	src := NewNodeSource(c, "node-1")
	entries, err := src.WorkloadPorts(ctx)
	if err != nil || entries != nil {
		t.Fatalf("expected nil entries and no error, got %v / %v", entries, err)
	}

	s := NewServer(c, "node-1", logr.Discard())
	if _, err := s.Add(ctx, &pb.AddRequest{
		PodNamespace: "ns",
		PodName:      "vm-launcher",
		ContainerId:  "cid-1",
		Port:         &pb.WorkloadPort{Interface: "cra0cid1"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err = src.WorkloadPorts(ctx)
	if err != nil {
		t.Fatalf("WorkloadPorts: %v", err)
	}
	if len(entries) != 1 || entries[0].Interface != "cra0cid1" {
		t.Fatalf("unexpected entries %+v", entries)
	}
}

func TestServeRefusesToRemoveNonSocketPath(t *testing.T) {
	regular := filepath.Join(t.TempDir(), "important.conf")
	if err := os.WriteFile(regular, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seeding file: %v", err)
	}

	s := NewServer(newFakeClient(t), "node-1", logr.Discard())
	if err := s.Serve(context.Background(), regular); err == nil {
		t.Fatal("expected Serve to refuse a path that is not a unix socket")
	}
	if _, err := os.Stat(regular); err != nil {
		t.Fatalf("Serve removed a path that was not a socket: %v", err)
	}
}

func TestServerAddRecordsLayer2Trunk(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()
	s := NewServer(c, "node-1", logr.Discard(), WithLayer2Namespace("tenant-a"))

	if _, err := s.Add(ctx, &pb.AddRequest{
		PodNamespace: "ns",
		PodName:      "vnf",
		ContainerId:  "cid-1",
		Port:         &pb.WorkloadPort{Interface: "cra0cid1"},
		Layer2Trunk: []*pb.Layer2TrunkMember{
			{Ref: &pb.Layer2AttachmentRef{Name: "green"}},
			{Ref: &pb.Layer2AttachmentRef{Name: "red"}, Vlan: 200},
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	ports := getNRP(t, c, "node-1").Spec.Ports
	if len(ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(ports))
	}
	entry := &ports[0]
	if entry.Layer2AttachmentRef != nil {
		t.Fatalf("expected no access ref, got %+v", entry.Layer2AttachmentRef)
	}
	if len(entry.Layer2Trunk) != 2 {
		t.Fatalf("expected 2 trunk members, got %+v", entry.Layer2Trunk)
	}
	// The wire carries bare names; the configured namespace is stamped on.
	for i := range entry.Layer2Trunk {
		if ns := entry.Layer2Trunk[i].Namespace; ns != "tenant-a" {
			t.Fatalf("member %d: expected namespace %q, got %q", i, "tenant-a", ns)
		}
	}
	// vlan 0 on the wire means "inherit the domain's own id", which stays
	// unresolved until the merge sees the NodeNetworkConfig.
	if entry.Layer2Trunk[0].VLAN != nil {
		t.Fatalf("expected an inherited vlan, got %d", *entry.Layer2Trunk[0].VLAN)
	}
	if entry.Layer2Trunk[1].VLAN == nil || *entry.Layer2Trunk[1].VLAN != 200 {
		t.Fatalf("expected vlan 200, got %v", entry.Layer2Trunk[1].VLAN)
	}
}

func TestServerAddStampsLayer2Namespace(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()
	// No option: references are resolved in the default intent namespace.
	s := NewServer(c, "node-1", logr.Discard())

	if _, err := s.Add(ctx, &pb.AddRequest{
		PodNamespace:        "ns",
		PodName:             "vnf",
		ContainerId:         "cid-1",
		Port:                &pb.WorkloadPort{Interface: "cra0cid1"},
		Layer2AttachmentRef: &pb.Layer2AttachmentRef{Name: "blue"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	ports := getNRP(t, c, "node-1").Spec.Ports
	if len(ports) != 1 || ports[0].Layer2AttachmentRef == nil {
		t.Fatalf("unexpected ports %+v", ports)
	}
	ref := ports[0].Layer2AttachmentRef
	if ref.Name != "blue" || ref.Namespace != DefaultLayer2Namespace {
		t.Fatalf("unexpected ref %+v", ref)
	}
}

// TestServerAddRecordsRequestedMTU covers the requested MTU reaching the
// recorded attachment, and an unset one becoming the default rather than a zero
// each renderer would have to interpret for itself.
func TestServerAddRecordsRequestedMTU(t *testing.T) {
	for _, tc := range []struct {
		name string
		mtu  uint32
		want uint16
	}{
		{name: "explicit", mtu: 9000, want: 9000},
		{name: "unset defaults", mtu: 0, want: DefaultPortMTU},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeClient(t)
			s := NewServer(c, "node-1", logr.Discard())

			if _, err := s.Add(context.Background(), &pb.AddRequest{
				PodNamespace: "ns", PodName: "pod", ContainerId: "c1",
				Port: &pb.WorkloadPort{Interface: "cra012345", Mtu: tc.mtu},
			}); err != nil {
				t.Fatalf("Add: %v", err)
			}
			ports := getNRP(t, c, "node-1").Spec.Ports
			if len(ports) != 1 || ports[0].MTU != tc.want {
				t.Fatalf("recorded ports = %+v, want mtu %d", ports, tc.want)
			}
		})
	}
}

// TestServerAddRejectsOutOfRangeMTU covers a request asking for a size no
// datapath could configure being refused at the door.
func TestServerAddRejectsOutOfRangeMTU(t *testing.T) {
	for _, mtu := range []uint32{68, 65535} {
		c := newFakeClient(t)
		s := NewServer(c, "node-1", logr.Discard())

		_, err := s.Add(context.Background(), &pb.AddRequest{
			PodNamespace: "ns", PodName: "pod", ContainerId: "c1",
			Port: &pb.WorkloadPort{Interface: "cra012345", Mtu: mtu},
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("Add with mtu %d = %v, want InvalidArgument", mtu, err)
		}
	}
}
