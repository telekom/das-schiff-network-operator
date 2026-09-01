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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

// seedLayer2s stores a NodeNetworkConfig for node carrying one Layer2 per
// stamped Layer2Attachment reference (name@namespace → vlan), which is the
// precondition an L2 ADD is checked against.
func seedLayer2s(ctx context.Context, t *testing.T, c client.Client, node string,
	refs map[v1alpha1.Layer2AttachmentRef]uint16,
) {
	t.Helper()
	cfg := &v1alpha1.NodeNetworkConfig{}
	cfg.Name = node
	cfg.Spec.Layer2s = map[string]v1alpha1.Layer2{}
	for ref, vlan := range refs {
		r := ref
		cfg.Spec.Layer2s[fmt.Sprintf("l2.%d", vlan)] = v1alpha1.Layer2{VLAN: vlan, AttachmentRef: &r}
	}
	if err := c.Create(ctx, cfg); err != nil {
		t.Fatalf("seeding NodeNetworkConfig: %v", err)
	}
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

// TestServerWriteSettlesAfterCallerGaveUp covers the compensating-DEL ordering
// the plugin relies on: an ADD whose caller times out mid-write still settles
// (the write is detached from the caller's context), and a DEL issued while
// that write is in flight waits for it and removes the entry — instead of
// reporting "nothing to remove" and letting the ADD persist a stale entry.
func TestServerWriteSettlesAfterCallerGaveUp(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding scheme: %v", err)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			// The first write (the ADD's) stalls until released, well past the
			// caller's cancellation.
			once.Do(func() {
				close(entered)
				<-release
			})
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("write with a dead context: %w", err)
			}
			return cl.Create(ctx, obj, opts...) //nolint:wrapcheck // pass-through interceptor
		},
	})
	s := NewServer(c, "node-1", logr.Discard())
	req := &pb.AddRequest{
		PodNamespace: "ns", PodName: "vm", ContainerId: "cid-1",
		Port: &pb.WorkloadPort{Interface: "cra0cid1"},
	}

	addCtx, cancelAdd := context.WithCancel(context.Background())
	addDone := make(chan error, 1)
	go func() {
		_, err := s.Add(addCtx, req)
		addDone <- err
	}()
	<-entered
	cancelAdd() // the plugin's call timed out; the server-side write is in flight

	delDone := make(chan error, 1)
	go func() {
		_, err := s.Del(context.Background(), &pb.DelRequest{ContainerId: "cid-1", Interface: "cra0cid1"})
		delDone <- err
	}()
	select {
	case err := <-delDone:
		t.Fatalf("DEL overtook the in-flight ADD (err=%v); it must wait for the write to settle", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-addDone; err != nil {
		t.Fatalf("ADD must settle although its caller went away: %v", err)
	}
	if err := <-delDone; err != nil {
		t.Fatalf("DEL after the settled ADD: %v", err)
	}
	if got := getNRP(t, base, "node-1").Spec.Ports; len(got) != 0 {
		t.Fatalf("DEL must remove the entry the ADD persisted, got %d ports", len(got))
	}
}

func TestServerAddAcceptsMaximumLengthInterface(t *testing.T) {
	c := newFakeClient(t)
	s := NewServer(c, "node-1", logr.Discard())

	if _, err := s.Add(context.Background(), &pb.AddRequest{
		PodNamespace: "ns",
		PodName:      "vm-launcher",
		ContainerId:  "cid-1",
		Port:         &pb.WorkloadPort{Interface: "cra012345678901"},
	}); err != nil {
		t.Fatalf("Add rejected a 15-character veth interface: %v", err)
	}
}

func TestServerAddValidatesTrunkSubinterfaceName(t *testing.T) {
	tests := []struct {
		name    string
		ifName  string
		vlan    uint32
		wantErr bool
	}{
		{
			name:    "rejects full bare name with four digit VLAN",
			ifName:  "cra012345678901",
			vlan:    4094,
			wantErr: true,
		},
		{
			name:   "accepts exact maximum VLAN capacity",
			ifName: "cra0123456",
			vlan:   4094,
		},
		{
			name:   "uses explicit VLAN length",
			ifName: "cra01234567",
			vlan:   100,
		},
		{
			name:    "reserves maximum capacity for inherited VLAN",
			ifName:  "cra01234567",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := newFakeClient(t)
			seedLayer2s(ctx, t, c, "node-1", map[v1alpha1.Layer2AttachmentRef]uint16{
				{Name: "blue", Namespace: DefaultLayer2Namespace}: 501,
			})
			s := NewServer(c, "node-1", logr.Discard())
			_, err := s.Add(ctx, &pb.AddRequest{
				PodNamespace: "ns",
				PodName:      "vm-launcher",
				ContainerId:  "cid-1",
				Port:         &pb.WorkloadPort{Interface: tc.ifName},
				Layer2Trunk: []*pb.Layer2TrunkMember{{
					Ref:  &pb.Layer2AttachmentRef{Name: "blue"},
					Vlan: tc.vlan,
				}},
			})
			if tc.wantErr {
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("Add error = %v, want InvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
		})
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

// TestServerAddRejectsReservedVRF ensures a request naming a platform-owned VRF
// (management/cluster) is refused up front rather than recorded.
func TestServerAddRejectsReservedVRF(t *testing.T) {
	c := newFakeClient(t)
	s := NewServer(c, "node-1", logr.Discard(), WithReservedVRFs("mgmt", "cluster", ""))
	ctx := context.Background()

	req := func(vrf string) *pb.AddRequest {
		return &pb.AddRequest{
			PodNamespace: "ns",
			PodName:      "vm",
			ContainerId:  "cid-1",
			Vrf:          vrf,
			Port:         &pb.WorkloadPort{Interface: "cra012345"},
		}
	}
	for _, vrf := range []string{"mgmt", "cluster"} {
		_, err := s.Add(ctx, req(vrf))
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("Add(vrf=%q) error = %v, want InvalidArgument", vrf, err)
		}
	}
	nrp := &v1alpha1.NodeWorkloadPorts{}
	if err := c.Get(ctx, types.NamespacedName{Name: "node-1"}, nrp); !apierrors.IsNotFound(err) {
		t.Fatalf("rejected attachment must not be recorded, got %+v (err %v)", nrp.Spec.Ports, err)
	}

	// The empty reserved name must not lock out the default table, and a tenant
	// VRF is unaffected.
	for _, vrf := range []string{"", "tenant-a"} {
		if _, err := s.Add(ctx, req(vrf)); err != nil {
			t.Fatalf("Add(vrf=%q): %v", vrf, err)
		}
	}
}

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
		"interface unsafe":     func(r *pb.AddRequest) { r.Port.Interface = "cra 01\n!" },
		"vrf too long":         func(r *pb.AddRequest) { r.Vrf = "tenant0123456789" },
		"vrf with whitespace":  func(r *pb.AddRequest) { r.Vrf = "tenant a" },
		"vrf with newline":     func(r *pb.AddRequest) { r.Vrf = "tenant\nrouter bgp 1" },
		"vrf with quote":       func(r *pb.AddRequest) { r.Vrf = "tenant\"" },
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
		"routable gateway v4":  func(r *pb.AddRequest) { r.Port.GatewayV4 = "10.0.0.1/32" },
		"routable gateway v6":  func(r *pb.AddRequest) { r.Port.GatewayV6 = "fd00::1/128" },
		"subnet gateway v6":    func(r *pb.AddRequest) { r.Port.GatewayV6 = "fe80::1/64" },
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

func TestServeRejectsRelativeSocketPath(t *testing.T) {
	s := NewServer(newFakeClient(t), "node-1", logr.Discard())
	if err := s.Serve(context.Background(), "workload.sock"); err == nil {
		t.Fatal("expected Serve to reject a relative socket path")
	}
}

func TestServeReturnsNilWhenContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Not t.TempDir(): unix socket paths are limited to ~104 bytes on some
	// platforms and the per-test directory name alone can exceed that.
	dir, err := os.MkdirTemp("/tmp", "wcni")
	if err != nil {
		t.Fatalf("creating socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	s := NewServer(newFakeClient(t), "node-1", logr.Discard())
	if err := s.Serve(ctx, filepath.Join(dir, "w.sock")); err != nil {
		t.Fatalf("expected a clean shutdown, got %v", err)
	}
}

func TestServerAddRecordsLayer2Trunk(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()
	seedLayer2s(ctx, t, c, "node-1", map[v1alpha1.Layer2AttachmentRef]uint16{
		{Name: "green", Namespace: "tenant-a"}: 501,
		{Name: "red", Namespace: "tenant-a"}:   502,
	})
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
	seedLayer2s(ctx, t, c, "node-1", map[v1alpha1.Layer2AttachmentRef]uint16{
		{Name: "blue", Namespace: DefaultLayer2Namespace}: 501,
	})
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

// TestServerAddRejectsUnstampedLayer2 covers the precondition on an L2 ADD: a
// Layer2Attachment that is not (yet) stamped onto the node's NodeNetworkConfig
// - typically because the intent reconciler is off or the L2A does not select
// the node - fails the ADD instead of being recorded and skipped forever.
func TestServerAddRejectsUnstampedLayer2(t *testing.T) {
	ctx := context.Background()
	access := &pb.AddRequest{
		PodNamespace:        "ns",
		PodName:             "vnf",
		ContainerId:         "cid-1",
		Port:                &pb.WorkloadPort{Interface: "cra0cid1"},
		Layer2AttachmentRef: &pb.Layer2AttachmentRef{Name: "blue"},
	}

	t.Run("no NodeNetworkConfig", func(t *testing.T) {
		c := newFakeClient(t)
		_, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, access)
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("Add error = %v, want FailedPrecondition", err)
		}
		nrp := &v1alpha1.NodeWorkloadPorts{}
		if err := c.Get(ctx, types.NamespacedName{Name: "node-1"}, nrp); !apierrors.IsNotFound(err) {
			t.Fatalf("rejected attachment must not be recorded, got %+v (err %v)", nrp.Spec.Ports, err)
		}
	})

	t.Run("Layer2Attachment not stamped", func(t *testing.T) {
		c := newFakeClient(t)
		seedLayer2s(ctx, t, c, "node-1", map[v1alpha1.Layer2AttachmentRef]uint16{
			{Name: "green", Namespace: DefaultLayer2Namespace}: 501,
		})
		_, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, access)
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("Add error = %v, want FailedPrecondition", err)
		}
		if msg := status.Convert(err).Message(); !strings.Contains(msg, "blue") ||
			!strings.Contains(msg, "enable-intent-reconciler") {
			t.Fatalf("error should name the reference and hint at the intent reconciler, got %q", msg)
		}
	})

	t.Run("one trunk member missing", func(t *testing.T) {
		c := newFakeClient(t)
		seedLayer2s(ctx, t, c, "node-1", map[v1alpha1.Layer2AttachmentRef]uint16{
			{Name: "green", Namespace: DefaultLayer2Namespace}: 501,
		})
		_, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, &pb.AddRequest{
			PodNamespace: "ns",
			PodName:      "vnf",
			ContainerId:  "cid-1",
			Port:         &pb.WorkloadPort{Interface: "cra0cid1"},
			Layer2Trunk: []*pb.Layer2TrunkMember{
				{Ref: &pb.Layer2AttachmentRef{Name: "green"}},
				{Ref: &pb.Layer2AttachmentRef{Name: "red"}, Vlan: 200},
			},
		})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("Add error = %v, want FailedPrecondition", err)
		}
	})

	t.Run("routed attachment needs no NodeNetworkConfig", func(t *testing.T) {
		c := newFakeClient(t)
		if _, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, &pb.AddRequest{
			PodNamespace: "ns",
			PodName:      "vm",
			ContainerId:  "cid-1",
			Port:         &pb.WorkloadPort{Interface: "cra0cid1"},
		}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	})
}

// TestServerAddRejectsMTUAboveLayer2 covers the second half of the L2
// precondition: a reference that resolves but to a domain that cannot carry the
// requested MTU is refused at ADD, since the merge would skip the entry for good.
func TestServerAddRejectsMTUAboveLayer2(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, c client.Client, mtus map[string]uint16) {
		t.Helper()
		cfg := &v1alpha1.NodeNetworkConfig{}
		cfg.Name = "node-1"
		cfg.Spec.Layer2s = map[string]v1alpha1.Layer2{}
		vlan := uint16(500)
		for name, mtu := range mtus {
			vlan++
			cfg.Spec.Layer2s[fmt.Sprintf("l2.%d", vlan)] = v1alpha1.Layer2{
				VLAN:          vlan,
				MTU:           mtu,
				AttachmentRef: &v1alpha1.Layer2AttachmentRef{Name: name, Namespace: DefaultLayer2Namespace},
			}
		}
		if err := c.Create(ctx, cfg); err != nil {
			t.Fatalf("seeding NodeNetworkConfig: %v", err)
		}
	}
	access := func(mtu uint32) *pb.AddRequest {
		return &pb.AddRequest{
			PodNamespace:        "ns",
			PodName:             "vnf",
			ContainerId:         "cid-1",
			Port:                &pb.WorkloadPort{Interface: "cra0cid1", Mtu: mtu},
			Layer2AttachmentRef: &pb.Layer2AttachmentRef{Name: "blue"},
		}
	}

	t.Run("access above domain mtu", func(t *testing.T) {
		c := newFakeClient(t)
		seed(t, c, map[string]uint16{"blue": 1500})
		_, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, access(9000))
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("Add error = %v, want FailedPrecondition", err)
		}
		if msg := status.Convert(err).Message(); !strings.Contains(msg, "9000") || !strings.Contains(msg, "1500") {
			t.Fatalf("error should name both MTUs, got %q", msg)
		}
		nrp := &v1alpha1.NodeWorkloadPorts{}
		if err := c.Get(ctx, types.NamespacedName{Name: "node-1"}, nrp); !apierrors.IsNotFound(err) {
			t.Fatalf("rejected attachment must not be recorded, got %+v (err %v)", nrp.Spec.Ports, err)
		}
	})

	t.Run("default mtu above domain mtu", func(t *testing.T) {
		c := newFakeClient(t)
		seed(t, c, map[string]uint16{"blue": 1400})
		if _, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, access(0)); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("Add error = %v, want FailedPrecondition for implicit %d", err, DefaultPortMTU)
		}
	})

	t.Run("access within domain mtu", func(t *testing.T) {
		c := newFakeClient(t)
		seed(t, c, map[string]uint16{"blue": 9000})
		if _, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, access(9000)); err != nil {
			t.Fatalf("Add: %v", err)
		}
	})

	t.Run("unconstrained domain", func(t *testing.T) {
		c := newFakeClient(t)
		seed(t, c, map[string]uint16{"blue": 0})
		if _, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, access(9000)); err != nil {
			t.Fatalf("Add: %v", err)
		}
	})

	t.Run("trunk needs every member large enough", func(t *testing.T) {
		c := newFakeClient(t)
		seed(t, c, map[string]uint16{"blue": 1500, "green": 9000})
		trunk := &pb.AddRequest{
			PodNamespace: "ns",
			PodName:      "vnf",
			ContainerId:  "cid-1",
			Port:         &pb.WorkloadPort{Interface: "cra0cid1", Mtu: 1500},
			Layer2Trunk: []*pb.Layer2TrunkMember{
				{Ref: &pb.Layer2AttachmentRef{Name: "blue"}},
				{Ref: &pb.Layer2AttachmentRef{Name: "green"}},
			},
		}
		if _, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, trunk); err != nil {
			t.Fatalf("Add: %v", err)
		}
		// Every sub-interface inherits the port MTU, so the 9000-byte member
		// does not make the trunk safe for the 1500-byte one.
		trunk.Port.Mtu = 9000
		_, err := NewServer(c, "node-1", logr.Discard()).Add(ctx, trunk)
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("Add error = %v, want FailedPrecondition", err)
		}
		if msg := status.Convert(err).Message(); !strings.Contains(msg, "1500") || !strings.Contains(msg, "9000") {
			t.Fatalf("error should name both MTUs, got %q", msg)
		}
	})
}
