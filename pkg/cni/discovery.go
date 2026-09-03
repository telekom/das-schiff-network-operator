//go:build linux

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

package cni

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// netnsRunDir is the standard iproute2 location for named network namespaces.
const netnsRunDir = "/var/run/netns"

// procDir is the mount point of the proc filesystem, used to enumerate the
// network namespaces of running processes (for PID-based CRA netns discovery).
const procDir = "/proc"

// resolveCRANetnsPath resolves the filesystem path of the CRA network namespace
// from the NetConf.
//
// Resolution precedence:
//  1. an absolute path (e.g. /proc/<pid>/ns/net or a bind-mounted ns file);
//  2. a named namespace under /var/run/netns/<name>;
//  3. auto-discovery: the named namespace that owns the trunk interface.
func resolveCRANetnsPath(conf *NetConf) (string, error) {
	spec := strings.TrimSpace(conf.CRANetns)

	switch {
	case spec == "" || spec == "auto":
		return discoverCRANetnsByTrunk(conf.trunkInterface())
	case filepath.IsAbs(spec):
		if _, err := os.Stat(spec); err != nil {
			return "", fmt.Errorf("CRA netns path %q not accessible: %w", spec, err)
		}
		return spec, nil
	default:
		path := filepath.Join(netnsRunDir, spec)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("named CRA netns %q not found at %s: %w", spec, path, err)
		}
		return path, nil
	}
}

// discoverCRANetnsByTrunk locates the CRA network namespace by the interface
// named trunk (e.g. "hbn"). It searches, in order:
//
//  1. named network namespaces under /var/run/netns/ (iproute2 convention);
//  2. the network namespaces of running processes under /proc/<pid>/ns/net.
//
// The CRA (FRR / VSR) typically runs as a long-lived process whose netns is not
// bind-mounted under /var/run/netns, so the /proc scan is required to find it.
// This mirrors the CRA-VSR findWorkNSName heuristic (locate the netns by its
// trunk interface) and lets a single base-config value drive both flavors.
//
// The current network namespace (where the plugin runs — the host netns) is
// always excluded: the trunk is a veth pair whose node-side end carries the same
// name as the CRA-side end, so the host netns also owns an interface named
// trunk. The CRA netns is by definition a *different* namespace (the port is
// moved across the netns boundary), so skipping the current netns disambiguates.
func discoverCRANetnsByTrunk(trunk string) (string, error) {
	currentInode := currentNetnsInode()

	if path, ok := searchNamedNetnsByTrunk(trunk, currentInode); ok {
		return path, nil
	}
	if path, ok := searchProcNetnsByTrunk(trunk, currentInode); ok {
		return path, nil
	}
	return "", fmt.Errorf("failed to auto-discover CRA netns: no other namespace or process owns interface %q", trunk)
}

// currentNetnsInode returns the inode of the network namespace the plugin runs
// in, or 0 if it cannot be determined. It is used to exclude the current
// (host) netns from trunk-based discovery.
func currentNetnsInode() uint64 {
	var st syscall.Stat_t
	if err := syscall.Stat("/proc/self/ns/net", &st); err != nil {
		return 0
	}
	return st.Ino
}

// netnsInode returns the inode of the network namespace referenced by path, or
// 0 if it cannot be determined.
func netnsInode(path string) uint64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0
	}
	return st.Ino
}

// searchNamedNetnsByTrunk scans /var/run/netns for a namespace that owns the
// trunk interface and returns its path. The namespace whose inode matches
// skipInode (the current/host netns) is ignored.
func searchNamedNetnsByTrunk(trunk string, skipInode uint64) (string, bool) {
	entries, err := os.ReadDir(netnsRunDir)
	if err != nil {
		return "", false
	}

	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(netnsRunDir, name)
		if skipInode != 0 && netnsInode(path) == skipInode {
			continue
		}
		handle, err := netns.GetFromName(name)
		if err != nil {
			continue
		}
		found := nsHasInterface(handle, trunk)
		handle.Close()
		if found {
			return path, true
		}
	}
	return "", false
}

// searchProcNetnsByTrunk scans the network namespaces of running processes
// (/proc/<pid>/ns/net) for the one that owns the trunk interface and returns
// the /proc path to that namespace. Namespaces are de-duplicated by their inode
// so each distinct netns is probed at most once; the current/host netns
// (skipInode) is ignored.
func searchProcNetnsByTrunk(trunk string, skipInode uint64) (string, bool) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return "", false
	}

	seen := make(map[uint64]struct{})
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue // not a PID directory
		}

		nsPath := filepath.Join(procDir, e.Name(), "ns", "net")
		var st syscall.Stat_t
		if err := syscall.Stat(nsPath, &st); err != nil {
			continue
		}
		if skipInode != 0 && st.Ino == skipInode {
			continue // current/host netns — the trunk's node-side end lives here
		}
		if _, dup := seen[st.Ino]; dup {
			continue
		}
		seen[st.Ino] = struct{}{}

		handle, err := netns.GetFromPath(nsPath)
		if err != nil {
			continue
		}
		found := nsHasInterface(handle, trunk)
		handle.Close()
		if found {
			return nsPath, true
		}
	}
	return "", false
}

// nsHasInterface reports whether the given namespace contains an interface with
// the provided name.
func nsHasInterface(handle netns.NsHandle, ifname string) bool {
	nlh, err := netlink.NewHandleAt(handle)
	if err != nil {
		return false
	}
	defer nlh.Close()

	_, err = nlh.LinkByName(ifname)
	return err == nil
}
