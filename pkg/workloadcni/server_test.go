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
