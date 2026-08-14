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

package deviceplugin

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// fakeKubelet is just enough of kubelet to accept a registration: a unix socket
// named kubelet.sock serving the Registration service.
type fakeKubelet struct {
	pluginapi.UnimplementedRegistrationServer

	mu         sync.Mutex
	registered []*pluginapi.RegisterRequest
	notify     chan struct{}

	server *grpc.Server
	dir    string
}

func newFakeKubelet(t *testing.T, dir string) *fakeKubelet {
	t.Helper()
	k := &fakeKubelet{notify: make(chan struct{}, 16), dir: dir}
	k.start(t)
	return k
}

func (k *fakeKubelet) start(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(k.dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(k.dir, kubeletSocketName)
	_ = os.Remove(path)
	var lc net.ListenConfig
	lis, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("listening on fake kubelet socket: %v", err)
	}
	k.server = grpc.NewServer()
	pluginapi.RegisterRegistrationServer(k.server, k)
	go func() { _ = k.server.Serve(lis) }()
	t.Cleanup(k.stop)
}

func (k *fakeKubelet) stop() {
	if k.server != nil {
		k.server.Stop()
		k.server = nil
	}
	_ = os.Remove(filepath.Join(k.dir, kubeletSocketName))
}

func (k *fakeKubelet) Register(_ context.Context, req *pluginapi.RegisterRequest) (*pluginapi.Empty, error) {
	k.mu.Lock()
	k.registered = append(k.registered, req)
	k.mu.Unlock()
	select {
	case k.notify <- struct{}{}:
	default:
	}
	return &pluginapi.Empty{}, nil
}

func (k *fakeKubelet) names() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	names := make([]string, 0, len(k.registered))
	for _, r := range k.registered {
		names = append(names, r.GetResourceName())
	}
	return names
}

// waitRegistrations blocks until at least n registrations have arrived.
func (k *fakeKubelet) waitRegistrations(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		k.mu.Lock()
		got := len(k.registered)
		k.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-k.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %d registrations, got %d (%v)", n, got, k.names())
		}
	}
}

// TestRunRegistersBothResourcesAndSurvivesKubeletRestart exercises the whole
// lifecycle against a fake kubelet. The restart half is the point: kubelet
// forgets every plugin when it restarts, so a plugin that does not re-register
// leaves the node advertising zero devices until the DaemonSet is bounced --
// a failure mode that unit tests of Allocate cannot see.
func TestRunRegistersBothResourcesAndSurvivesKubeletRestart(t *testing.T) {
	cfg := testConfig(t)
	kubelet := newFakeKubelet(t, cfg.KubeletSocketDir)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- Run(ctx, cfg, logr.Discard()) }()

	kubelet.waitRegistrations(t, 2)
	names := kubelet.names()
	want := map[string]bool{
		cfg.ResourceName(ResourceVhostUser):  false,
		cfg.ResourceName(ResourceVirtioUser): false,
	}
	for _, n := range names {
		if _, ok := want[n]; !ok {
			t.Fatalf("unexpected resource %q registered", n)
		}
		want[n] = true
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("resource %q was never registered (got %v)", n, names)
		}
	}

	// Both socket trees must exist even before the first Allocate, because the
	// CRA container mounts them at start.
	for _, dir := range []string{cfg.VhostUserDir, cfg.VirtioUserDir} {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Fatalf("socket tree %s not created: %v", dir, err)
		}
	}

	// Restart kubelet: drop the socket, then recreate it.
	kubelet.stop()
	time.Sleep(100 * time.Millisecond)
	kubelet.start(t)

	kubelet.waitRegistrations(t, 4)

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on context cancellation", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// TestPluginServesListAndWatchAndAllocate drives one plugin over its real gRPC
// endpoint, the way kubelet does.
func TestPluginServesListAndWatchAndAllocate(t *testing.T) {
	cfg := testConfig(t)
	kubelet := newFakeKubelet(t, cfg.KubeletSocketDir)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	p := NewPlugin(cfg, ResourceVirtioUser, logr.Discard())
	serveErr := make(chan error, 1)
	go func() { serveErr <- p.Serve(ctx) }()
	kubelet.waitRegistrations(t, 1)

	if got := kubelet.registered[0].GetEndpoint(); got != filepath.Base(p.socketPath) {
		t.Errorf("registered endpoint = %q, want the bare filename %q", got, filepath.Base(p.socketPath))
	}

	conn, err := grpc.NewClient("unix://"+p.socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dialling plugin: %v", err)
	}
	defer conn.Close()
	client := pluginapi.NewDevicePluginClient(conn)

	stream, err := client.ListAndWatch(ctx, &pluginapi.Empty{})
	if err != nil {
		t.Fatalf("ListAndWatch: %v", err)
	}
	list, err := stream.Recv()
	if err != nil {
		t.Fatalf("receiving device list: %v", err)
	}
	if len(list.GetDevices()) != cfg.DeviceCount {
		t.Fatalf("got %d devices, want %d", len(list.GetDevices()), cfg.DeviceCount)
	}
	deviceID := list.GetDevices()[0].GetID()
	if list.GetDevices()[0].GetHealth() != pluginapi.Healthy {
		t.Errorf("device is not healthy")
	}

	resp, err := client.Allocate(ctx, &pluginapi.AllocateRequest{
		ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIds: []string{deviceID}}},
	})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	mount := resp.GetContainerResponses()[0].GetMounts()[0]
	if want := filepath.Join(cfg.VhostUserDir, deviceID); mount.GetHostPath() != want {
		t.Errorf("host path = %q, want %q", mount.GetHostPath(), want)
	}
	if want := filepath.Join(cfg.VirtioUserDir, "0"); mount.GetContainerPath() != want {
		t.Errorf("container path = %q, want %q", mount.GetContainerPath(), want)
	}

	// Stop must take the endpoint away so a restarted plugin can bind it again.
	p.Stop()
	if _, err := os.Stat(p.socketPath); !os.IsNotExist(err) {
		t.Errorf("registration socket still present after Stop: %v", err)
	}
	select {
	case <-serveErr:
	case <-time.After(20 * time.Second):
		t.Fatal("Serve did not return after Stop")
	}
}

// TestServeRemovesStaleSocket covers the restart-in-place case: the DaemonSet
// container is killed without a chance to clean up, and the leftover socket
// would otherwise make Listen fail with EADDRINUSE forever.
func TestServeRemovesStaleSocket(t *testing.T) {
	cfg := testConfig(t)
	newFakeKubelet(t, cfg.KubeletSocketDir)

	p := NewPlugin(cfg, ResourceVirtioUser, logr.Discard())
	var lc net.ListenConfig
	lis, err := lc.Listen(t.Context(), "unix", p.socketPath)
	if err != nil {
		t.Fatalf("creating stale socket: %v", err)
	}
	lis.Close() // leaves the socket file behind

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- p.Serve(ctx) }()

	select {
	case err := <-serveErr:
		t.Fatalf("Serve returned early: %v", err)
	case <-time.After(2 * time.Second):
	}
	p.Stop()
}

// TestRunKeepsRetryingAResourceThatCannotServe is the partial-failure case.
// Each resource is served by its own goroutine, and one that gave up would be
// invisible: the healthy resource keeps working, the DaemonSet pod stays
// Running, nothing is logged a second time, and the only symptom is that pods
// requesting the dead resource stay Pending with a message about the scheduler
// rather than about the plugin.
//
// So the failing resource must keep retrying (and keep saying so), while the
// healthy one must be unaffected -- one broken resource must not take a working
// one down with it.
func TestRunKeepsRetryingAResourceThatCannotServe(t *testing.T) {
	cfg := testConfig(t)
	kubelet := newFakeKubelet(t, cfg.KubeletSocketDir)

	// A regular file where the vhost-user plugin wants its endpoint: Serve
	// refuses to unlink a non-socket, so that resource can never come up while
	// virtio-user is perfectly healthy.
	blocked := filepath.Join(cfg.KubeletSocketDir, "dsno-"+ResourceVhostUser+".sock")
	if err := os.WriteFile(blocked, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var mu sync.Mutex
	var failures int
	log := funcr.New(func(_, args string) {
		if strings.Contains(args, "device plugin stopped, retrying") &&
			strings.Contains(args, ResourceVhostUser) {
			mu.Lock()
			failures++
			mu.Unlock()
		}
	}, funcr.Options{})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- Run(ctx, cfg, log) }()

	// The healthy resource must serve regardless of its sibling.
	kubelet.waitRegistrations(t, 1)
	if got := kubelet.names(); len(got) != 1 || got[0] != cfg.ResourceName(ResourceVirtioUser) {
		t.Fatalf("registered resources = %v, want only the healthy one", got)
	}

	// And the broken one must still be trying, not silently given up on.
	deadline := time.After(15 * time.Second)
	for {
		mu.Lock()
		n := failures
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("vhost-user was retried %d times; a resource that cannot serve must keep retrying", n)
		case <-time.After(100 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run returned %v, want nil on context cancellation", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
