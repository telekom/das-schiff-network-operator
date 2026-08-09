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
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// listenSocket creates a unix socket the way the fast path does: as a plain
// socket file in an already-existing per-device directory, with a mode the
// workload cannot connect through.
func listenSocket(t *testing.T, dir, deviceID string, mode os.FileMode) string {
	t.Helper()
	devDir := filepath.Join(dir, deviceID)
	if err := os.MkdirAll(devDir, dirMode); err != nil {
		t.Fatalf("mkdir %s: %v", devDir, err)
	}
	path := filepath.Join(devDir, SocketFile)
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { l.Close() })
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

// TestAdoptSocketMakesTheSocketConnectable is the regression test for a VM that
// could not start: grout creates its vhost-user socket as root with its own
// umask (0755), so the qemu user got EACCES on connect and KubeVirt reported
// "Failed to connect to '...': Permission denied". Only the mode can be
// asserted portably -- the test does not run as root, so testConfig pins the
// owner to the current user -- but the mode is the half that was wrong.
func TestAdoptSocketMakesTheSocketConnectable(t *testing.T) {
	cfg := testConfig(t)
	path := listenSocket(t, cfg.VhostUserDir, "abc123", 0o755)

	if err := adoptSocket(path, cfg, logr.Discard()); err != nil {
		t.Fatalf("adoptSocket: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if got := info.Mode().Perm(); got != socketFileMode {
		t.Errorf("mode = %#o, want %#o", got, socketFileMode)
	}
}

// TestAdoptSocketIgnoresNonSockets guards the tree the plugin shares with the
// CRA container: a regular file or a symlink planted in a per-device directory
// must not be chowned.
func TestAdoptSocketIgnoresNonSockets(t *testing.T) {
	cfg := testConfig(t)
	devDir := filepath.Join(cfg.VhostUserDir, "notasocket")
	if err := os.MkdirAll(devDir, dirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(devDir, SocketFile)
	if err := os.WriteFile(path, []byte("regular file"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := adoptSocket(path, cfg, logr.Discard()); err != nil {
		t.Fatalf("adoptSocket: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %#o, want it untouched at 0600", got)
	}
}

// TestAdoptSocketToleratesAMissingSocket covers the normal case: Allocate()
// creates the directory well before the fast path programs the port, so most
// sweeps find a directory with nothing in it.
func TestAdoptSocketToleratesAMissingSocket(t *testing.T) {
	cfg := testConfig(t)
	devDir := filepath.Join(cfg.VhostUserDir, "empty")
	if err := os.MkdirAll(devDir, dirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := adoptSocket(filepath.Join(devDir, SocketFile), cfg, logr.Discard()); err != nil {
		t.Errorf("adoptSocket on a directory with no socket yet: %v", err)
	}
}

// TestSweepAdoptsOnlyTheFastPathTree locks which tree is swept. The other tree
// holds sockets the WORKLOAD creates as itself; adopting those would take a
// socket away from a workload that does not run as the socket owner.
func TestSweepAdoptsOnlyTheFastPathTree(t *testing.T) {
	cfg := testConfig(t)
	fastPath := listenSocket(t, cfg.VhostUserDir, "fastpath", 0o755)
	workload := listenSocket(t, cfg.VirtioUserDir, "workload", 0o755)

	if err := sweepFastPathSockets(cfg, logr.Discard()); err != nil {
		t.Fatalf("sweepFastPathSockets: %v", err)
	}

	fpInfo, err := os.Lstat(fastPath)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if got := fpInfo.Mode().Perm(); got != socketFileMode {
		t.Errorf("fast-path socket mode = %#o, want %#o", got, socketFileMode)
	}
	wlInfo, err := os.Lstat(workload)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if got := wlInfo.Mode().Perm(); got != 0o755 {
		t.Errorf("workload socket mode = %#o, want it untouched at 0755", got)
	}
}

// TestAdoptFastPathSocketsAdoptsASocketCreatedLater is the timing the sweep
// exists for: the directory is allocated first and the socket only appears once
// the CRA has programmed the port, which is after the sweep has already run
// over an empty directory.
func TestAdoptFastPathSocketsAdoptsASocketCreatedLater(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go adoptFastPathSockets(ctx, cfg, logr.Discard())

	path := listenSocket(t, cfg.VhostUserDir, "later", 0o755)

	deadline := time.Now().Add(10 * time.Second)
	for {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("Lstat: %v", err)
		}
		if info.Mode().Perm() == socketFileMode {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket mode = %#o, want %#o", info.Mode().Perm(), socketFileMode)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
