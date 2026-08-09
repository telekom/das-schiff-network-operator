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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-logr/logr"
)

const (
	// socketFileMode is the mode a fast-path-created socket is given. Owner and
	// group are the socket owner, and connect(2) on a unix socket needs WRITE
	// permission on the file, so group-write is the whole point of the mode.
	socketFileMode = 0o660

	// socketSweepInterval paces the ownership sweep. It has to be short: the
	// window it closes is between the fast path creating the socket and the
	// workload connecting to it, and both happen within a second of the pod
	// being admitted.
	socketSweepInterval = time.Second
)

// adoptFastPathSockets keeps ownership of the sockets the FAST PATH creates in
// line with the configured socket owner, until ctx is cancelled.
//
// Only one of the two trees needs this. Where the workload is the vhost-user
// server it creates the socket itself, as itself, in a directory Allocate()
// already handed it -- nothing to fix, and chowning it would take the socket
// away from a workload that does not happen to run as the socket owner. Where
// the FAST PATH is the server (a workload holding virtio-user, e.g. a KubeVirt
// VM), the socket is created by the CRA as root, with the CRA's umask:
//
//	srwxr-xr-x 1 root root socket
//
// The directory is owned correctly -- Allocate() saw to that -- but the socket
// inside it is not, and a directory's ownership does not propagate to a file
// created in it. So QEMU, which KubeVirt runs as the qemu user, gets EACCES on
// connect and the VM dies with "Failed to connect to '...': Permission denied".
// Neither DPDK's net_vhost nor grout expose a mode for the socket they create,
// so the only lever left is to adopt the socket after the fact.
//
// This lives in the device plugin rather than in the node agent that programs
// the port because ownership is the plugin's contract: it is the component that
// creates the directories, that is configured with the uid/gid, and that
// already carries CAP_CHOWN. The agent would have to be told the same uid/gid
// through a second, independently-set flag.
//
// Sweeping rather than watching is deliberate. The recovery this needs is
// convergent, not event-driven: a socket the fast path recreates (a CRA
// restart, a port re-added after a config change) has to be adopted again, and
// a missed inotify event would strand a workload that looks correctly
// configured. A directory listing per second over a handful of entries costs
// nothing next to that.
func adoptFastPathSockets(ctx context.Context, cfg *Config, log logr.Logger) {
	ticker := time.NewTicker(socketSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := sweepFastPathSockets(cfg, log); err != nil {
				log.Error(err, "adopting fast-path sockets", "tree", cfg.VhostUserDir)
			}
		}
	}
}

// sweepFastPathSockets adopts every socket in the fast-path socket tree.
func sweepFastPathSockets(cfg *Config, log logr.Logger) error {
	entries, err := os.ReadDir(cfg.VhostUserDir)
	if err != nil {
		return fmt.Errorf("reading socket tree %s: %w", cfg.VhostUserDir, err)
	}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(cfg.VhostUserDir, entry.Name(), SocketFile)
		if err := adoptSocket(path, cfg, log); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// adoptSocket gives one socket to the socket owner, if it exists and is not
// already owned correctly. A directory without a socket is not an error: the
// directory is created at Allocate() time and the socket only appears once the
// fast path has programmed the port.
func adoptSocket(path string, cfg *Config, log logr.Logger) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	// Anything that is not a socket is not ours to touch, and a symlink must
	// never be followed: the tree is shared with the CRA container.
	if info.Mode()&os.ModeSocket == 0 {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && int(stat.Uid) == cfg.SocketOwnerUID && int(stat.Gid) == cfg.SocketOwnerGID &&
		info.Mode().Perm() == socketFileMode {
		return nil
	}

	if err := os.Chown(path, cfg.SocketOwnerUID, cfg.SocketOwnerGID); err != nil {
		return fmt.Errorf("chowning %s to %d:%d: %w", path, cfg.SocketOwnerUID, cfg.SocketOwnerGID, err)
	}
	if err := os.Chmod(path, socketFileMode); err != nil {
		return fmt.Errorf("setting mode on %s: %w", path, err)
	}
	log.Info("adopted fast-path socket", "path", path,
		"uid", cfg.SocketOwnerUID, "gid", cfg.SocketOwnerGID, "mode", fmt.Sprintf("%#o", socketFileMode))
	return nil
}
