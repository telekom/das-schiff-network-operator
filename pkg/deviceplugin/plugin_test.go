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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// testConfig returns a config whose every path is under a scratch directory,
// which is precisely what making the paths configurable buys us.
func testConfig(t *testing.T) *Config {
	t.Helper()
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.VhostUserDir = filepath.Join(root, "vhost-user")
	cfg.VirtioUserDir = filepath.Join(root, "virtio-user")
	cfg.KubeletSocketDir = filepath.Join(root, "kubelet")
	cfg.DeviceInfoDir = filepath.Join(root, "devinfo")
	cfg.DeviceCount = 4
	// The test process is not root, so it cannot chown to qemu.
	cfg.SocketOwnerUID = os.Getuid()
	cfg.SocketOwnerGID = os.Getgid()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return &cfg
}

// TestSocketTreesAreCrossed locks the single most breakable part of the
// contract: the host path and the pod path of one socket live in DIFFERENT
// trees, and which is which depends on the resource. Swapping them produces a
// socket that silently never connects.
func TestSocketTreesAreCrossed(t *testing.T) {
	cfg := testConfig(t)

	hostTree, podTree := cfg.socketTrees(ResourceVirtioUser)
	if hostTree != cfg.VhostUserDir || podTree != cfg.VirtioUserDir {
		t.Errorf("virtio-user trees = (%q, %q), want (%q, %q): a virtio-user workload connects to the "+
			"socket the fast path serves", hostTree, podTree, cfg.VhostUserDir, cfg.VirtioUserDir)
	}

	hostTree, podTree = cfg.socketTrees(ResourceVhostUser)
	if hostTree != cfg.VirtioUserDir || podTree != cfg.VhostUserDir {
		t.Errorf("vhost-user trees = (%q, %q), want (%q, %q)", hostTree, podTree, cfg.VirtioUserDir, cfg.VhostUserDir)
	}
}

func TestConfigValidateRejectsBadPaths(t *testing.T) {
	cases := map[string]func(*Config){
		"relative socket tree":  func(c *Config) { c.VhostUserDir = "relative/path" },
		"identical trees":       func(c *Config) { c.VirtioUserDir = c.VhostUserDir },
		"identical after clean": func(c *Config) { c.VirtioUserDir = c.VhostUserDir + "/." },
		"unknown resource":      func(c *Config) { c.Resources = []string{"nonsense"} },
		"negative device count": func(c *Config) { c.DeviceCount = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() = nil, want error")
			}
		})
	}
}

func TestConfigValidateFillsDefaults(t *testing.T) {
	cfg := Config{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.ResourceDomain != DefaultResourceDomain || cfg.VhostUserDir != DefaultVhostUserDir ||
		cfg.VirtioUserDir != DefaultVirtioUserDir || cfg.DeviceCount != DefaultDeviceCount {
		t.Fatalf("Validate did not fill defaults: %+v", cfg)
	}
	if len(cfg.Resources) != 2 {
		t.Fatalf("Resources = %v, want both", cfg.Resources)
	}
}

// TestAllocateCrossesPathsAndPublishesPodPath is the core behaviour: the mount
// takes the host directory to the pod-side ordinal directory, and the
// device-info file -- which the KubeVirt hook turns into domain XML -- must
// carry the POD-side path, never the host one.
func TestAllocateCrossesPathsAndPublishesPodPath(t *testing.T) {
	cfg := testConfig(t)
	p := NewPlugin(cfg, ResourceVirtioUser, logr.Discard())

	resp, err := p.Allocate(t.Context(), &pluginapi.AllocateRequest{
		ContainerRequests: []*pluginapi.ContainerAllocateRequest{
			{DevicesIds: []string{"aabbccddee"}},
		},
	})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if len(resp.GetContainerResponses()) != 1 {
		t.Fatalf("got %d container responses, want 1", len(resp.GetContainerResponses()))
	}
	cresp := resp.GetContainerResponses()[0]

	if len(cresp.GetMounts()) != 1 {
		t.Fatalf("got %d mounts, want 1", len(cresp.GetMounts()))
	}
	mount := cresp.GetMounts()[0]
	wantHost := filepath.Join(cfg.VhostUserDir, "aabbccddee")
	wantPod := filepath.Join(cfg.VirtioUserDir, "0")
	if mount.GetHostPath() != wantHost {
		t.Errorf("mount host path = %q, want %q", mount.GetHostPath(), wantHost)
	}
	if mount.GetContainerPath() != wantPod {
		t.Errorf("mount container path = %q, want %q", mount.GetContainerPath(), wantPod)
	}
	if mount.GetReadOnly() {
		t.Error("mount is read-only; the workload must be able to create its socket in it")
	}

	// The host directory must exist: the fast path opens the socket there, and
	// kubelet's bind mount of a missing source would create it as root-owned.
	if fi, err := os.Stat(wantHost); err != nil || !fi.IsDir() {
		t.Fatalf("host socket dir %s not created: %v", wantHost, err)
	}

	if got := cresp.GetEnvs()[deviceEnvVar(ResourceVirtioUser)]; got != "aabbccddee" {
		t.Errorf("device env = %q, want %q", got, "aabbccddee")
	}

	infoPath := filepath.Join(cfg.DeviceInfoDir,
		deviceInfoFileName(cfg.ResourceName(ResourceVirtioUser), "aabbccddee"))
	data, err := os.ReadFile(infoPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("reading device info: %v", err)
	}
	var info deviceInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("parsing device info: %v", err)
	}
	if info.VhostUser == nil {
		t.Fatal("device info has no vhost-user section")
	}
	if want := filepath.Join(wantPod, SocketFile); info.VhostUser.Path != want {
		t.Errorf("device-info path = %q, want the POD-side path %q (the host path would give the VM a "+
			"domain XML pointing at a socket it cannot open)", info.VhostUser.Path, want)
	}
	if info.VhostUser.Mode != "client" {
		t.Errorf("device-info mode = %q, want %q for the virtio-user resource", info.VhostUser.Mode, "client")
	}
}

// TestAllocateNumbersPodIndicesPerContainer pins the ordinal rule the CNI and
// the KubeVirt hook both rely on: indices restart at 0 for every container, so
// a pod asking for one socket always sees it at .../0/socket.
func TestAllocateNumbersPodIndicesPerContainer(t *testing.T) {
	cfg := testConfig(t)
	p := NewPlugin(cfg, ResourceVirtioUser, logr.Discard())

	resp, err := p.Allocate(t.Context(), &pluginapi.AllocateRequest{
		ContainerRequests: []*pluginapi.ContainerAllocateRequest{
			{DevicesIds: []string{"dev0000000", "dev1111111"}},
			{DevicesIds: []string{"dev2222222"}},
		},
	})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	first := resp.GetContainerResponses()[0]
	if got, want := first.GetMounts()[0].GetContainerPath(), filepath.Join(cfg.VirtioUserDir, "0"); got != want {
		t.Errorf("first mount container path = %q, want %q", got, want)
	}
	if got, want := first.GetMounts()[1].GetContainerPath(), filepath.Join(cfg.VirtioUserDir, "1"); got != want {
		t.Errorf("second mount container path = %q, want %q", got, want)
	}
	if got, want := first.GetEnvs()[deviceEnvVar(ResourceVirtioUser)], "dev0000000,dev1111111"; got != want {
		t.Errorf("device env = %q, want %q", got, want)
	}

	second := resp.GetContainerResponses()[1]
	if got, want := second.GetMounts()[0].GetContainerPath(), filepath.Join(cfg.VirtioUserDir, "0"); got != want {
		t.Errorf("second container restarts at index 0, got %q want %q", got, want)
	}
}

// TestAllocateRejectsTraversingDeviceID guards the one place an id becomes a
// filesystem path.
func TestAllocateRejectsTraversingDeviceID(t *testing.T) {
	cfg := testConfig(t)
	p := NewPlugin(cfg, ResourceVirtioUser, logr.Discard())

	for _, id := range []string{"", "../../etc", "a/b", `a\b`} {
		if _, err := p.Allocate(t.Context(), &pluginapi.AllocateRequest{
			ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIds: []string{id}}},
		}); err == nil {
			t.Errorf("Allocate(%q) = nil error, want rejection", id)
		}
	}
}

// TestDeviceInfoFileNameMatchesNPWGConvention pins the filename Multus looks
// for; getting it wrong silently disables the staged-device-info path in the
// CNI, which then falls back to a positional guess.
func TestDeviceInfoFileNameMatchesNPWGConvention(t *testing.T) {
	got := deviceInfoFileName("network.t-caas.telekom.com/virtio-user", "abc123")
	want := "network.t-caas.telekom.com-virtio-user-abc123-device.json"
	if got != want {
		t.Errorf("deviceInfoFileName = %q, want %q", got, want)
	}
}

func TestDeviceEnvVar(t *testing.T) {
	if got, want := deviceEnvVar(ResourceVirtioUser), "DSNO_VIRTIO_USER_DEVICES"; got != want {
		t.Errorf("deviceEnvVar = %q, want %q", got, want)
	}
}

// TestNewPluginGeneratesDistinctDeviceIDs guards against two devices sharing a
// socket directory, which would silently cross-connect two pods.
func TestNewPluginGeneratesDistinctDeviceIDs(t *testing.T) {
	cfg := testConfig(t)
	p := NewPlugin(cfg, ResourceVhostUser, logr.Discard())

	seen := make(map[string]bool, len(p.devices))
	for _, d := range p.devices {
		if len(d.GetID()) != deviceIDLen {
			t.Fatalf("device id %q has length %d, want %d", d.GetID(), len(d.GetID()), deviceIDLen)
		}
		if seen[d.GetID()] {
			t.Fatalf("duplicate device id %q", d.GetID())
		}
		seen[d.GetID()] = true
	}
	if len(seen) != cfg.DeviceCount {
		t.Fatalf("got %d devices, want %d", len(seen), cfg.DeviceCount)
	}
}

// TestRemoveStaleSocketRefusesRegularFile keeps a misconfigured
// --kubelet-socket-dir from deleting real files on the host.
func TestRemoveStaleSocketRefusesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(path, []byte("important"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := removeStaleSocket(path); err == nil {
		t.Fatal("removeStaleSocket on a regular file = nil, want error")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("regular file was removed: %v", err)
	}
}
