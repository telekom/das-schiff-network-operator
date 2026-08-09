//go:build linux

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

package cni

import (
	"os"
	"path/filepath"
	"testing"
)

// defaultPodSocket is the pod-side socket path derived for allocation index 0.
const defaultPodSocket = "/run/vsr-virtio-user/0/socket"

// writeStagedDeviceInfo writes a device-plugin-shaped device-info file, as
// Multus stages for us at the CNIDeviceInfoFile path.
func writeStagedDeviceInfo(t *testing.T, path, mode, podPath string) {
	t.Helper()
	body := `{"type":"vhost-user","version":"1.1.0","vhost-user":{"mode":"` + mode + `","path":"` + podPath + `"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing staged device info: %v", err)
	}
}

func TestResolveVhostUserRequiresDeviceID(t *testing.T) {
	// A missing deviceID must fail loudly: every path we could invent names a
	// socket nothing is listening on.
	conf := &NetConf{Transport: TransportVhostUser}
	if _, err := resolveVhostUser(conf); err == nil {
		t.Fatal("expected an error without a deviceID, got nil")
	}
	// A static socketPath is not an escape hatch either.
	conf.SocketPath = "/run/vhost/net1.sock"
	if _, err := resolveVhostUser(conf); err == nil {
		t.Fatal("expected an error without a deviceID even with socketPath set, got nil")
	}
}

func TestResolveVhostUserPrefersRuntimeConfigDeviceID(t *testing.T) {
	conf := &NetConf{Transport: TransportVhostUser, DeviceID: "toplevel00"}
	conf.RuntimeConfig.DeviceID = "capability"
	att, err := resolveVhostUser(conf)
	if err != nil {
		t.Fatalf("resolveVhostUser: %v", err)
	}
	if att.DeviceID != "capability" {
		t.Errorf("DeviceID = %q, want the capability value", att.DeviceID)
	}
}

func TestResolveVhostUserDerivesPathsFromDeviceID(t *testing.T) {
	// Default resource is virtio-user: the workload is the virtio-user end, so
	// the host-side socket lives in the tree where vSR is the vhost-user backend.
	conf := &NetConf{Transport: TransportVhostUser, DeviceID: "3f9a2b1c7d"}
	att, err := resolveVhostUser(conf)
	if err != nil {
		t.Fatalf("resolveVhostUser: %v", err)
	}
	if want := "/run/vsr-vhost-user/3f9a2b1c7d/socket"; att.HostPath != want {
		t.Errorf("HostPath = %q, want %q", att.HostPath, want)
	}
	if want := defaultPodSocket; att.PodPath != want {
		t.Errorf("PodPath = %q, want %q", att.PodPath, want)
	}
	// virtio-user => the workload connects, so it is the client.
	if att.Mode != SocketModeClient {
		t.Errorf("Mode = %q, want %q for the virtio-user resource", att.Mode, SocketModeClient)
	}
}

func TestResolveVhostUserSwapsTreesForVhostUserResource(t *testing.T) {
	// A workload requesting vhost-user is the vhost-user end, so the trees swap.
	conf := &NetConf{
		Transport:      TransportVhostUser,
		DeviceID:       "3f9a2b1c7d",
		DeviceResource: "nc-k8s-plugin.6wind.com/vhost-user",
	}
	att, err := resolveVhostUser(conf)
	if err != nil {
		t.Fatalf("resolveVhostUser: %v", err)
	}
	if want := "/run/vsr-virtio-user/3f9a2b1c7d/socket"; att.HostPath != want {
		t.Errorf("HostPath = %q, want %q", att.HostPath, want)
	}
	if want := "/run/vsr-vhost-user/0/socket"; att.PodPath != want {
		t.Errorf("PodPath = %q, want %q", att.PodPath, want)
	}
	// vhost-user => the workload owns the socket, so it is the server.
	if att.Mode != SocketModeServer {
		t.Errorf("Mode = %q, want %q for the vhost-user resource", att.Mode, SocketModeServer)
	}
}

func TestResolveVhostUserStagedDeviceInfoWins(t *testing.T) {
	staged := filepath.Join(t.TempDir(), "cni-device-info.json")
	writeStagedDeviceInfo(t, staged, SocketModeClient, "/run/vsr-virtio-user/2/socket")

	conf := &NetConf{
		Transport:  TransportVhostUser,
		DeviceID:   "3f9a2b1c7d",
		SocketMode: SocketModeServer, // the NAD's claim must lose
	}
	conf.RuntimeConfig.CNIDeviceInfoFile = staged

	att, err := resolveVhostUser(conf)
	if err != nil {
		t.Fatalf("resolveVhostUser: %v", err)
	}
	// The plugin's own statement of what it allocated is authoritative for the
	// pod-side index and the socket mode.
	if want := "/run/vsr-virtio-user/2/socket"; att.PodPath != want {
		t.Errorf("PodPath = %q, want the staged %q", att.PodPath, want)
	}
	if att.Mode != SocketModeClient {
		t.Errorf("Mode = %q, want the staged %q", att.Mode, SocketModeClient)
	}
	// The host side is still derived: the device-info file only ever describes
	// the pod's view.
	if want := "/run/vsr-vhost-user/3f9a2b1c7d/socket"; att.HostPath != want {
		t.Errorf("HostPath = %q, want %q", att.HostPath, want)
	}
}

func TestResolveVhostUserIgnoresUnusableDeviceInfo(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing broken device info: %v", err)
	}

	// Multus's copy is best-effort, so an absent or malformed file must fall
	// back to derivation rather than fail.
	for name, path := range map[string]string{
		"missing": filepath.Join(dir, "absent.json"),
		"broken":  broken,
	} {
		t.Run(name, func(t *testing.T) {
			conf := &NetConf{Transport: TransportVhostUser, DeviceID: "3f9a2b1c7d"}
			conf.RuntimeConfig.CNIDeviceInfoFile = path
			att, err := resolveVhostUser(conf)
			if err != nil {
				t.Fatalf("resolveVhostUser: %v", err)
			}
			if want := defaultPodSocket; att.PodPath != want {
				t.Errorf("PodPath = %q, want the derived %q", att.PodPath, want)
			}
		})
	}
}

func TestResolveVhostUserSocketPathOverridesHostSideOnly(t *testing.T) {
	conf := &NetConf{
		Transport:  TransportVhostUser,
		DeviceID:   "3f9a2b1c7d",
		SocketPath: "/run/custom/vsr.sock",
	}
	att, err := resolveVhostUser(conf)
	if err != nil {
		t.Fatalf("resolveVhostUser: %v", err)
	}
	if att.HostPath != "/run/custom/vsr.sock" {
		t.Errorf("HostPath = %q, want the override", att.HostPath)
	}
	if want := defaultPodSocket; att.PodPath != want {
		t.Errorf("PodPath = %q, want %q (the override is host-side only)", att.PodPath, want)
	}
}

func TestResolveVhostUserRejectsTraversingDeviceID(t *testing.T) {
	// The deviceID names a directory under the socket tree; a traversing value
	// would move the CRA's socket outside the tree the operator granted it.
	for _, id := range []string{"../escape", "a/b", "..", ".", "/abs"} {
		conf := &NetConf{Transport: TransportVhostUser, DeviceID: id}
		if _, err := resolveVhostUser(conf); err == nil {
			t.Errorf("resolveVhostUser with deviceID %q = nil error, want a rejection", id)
		}
	}
}
