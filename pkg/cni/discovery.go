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
//  3. auto-discovery: the namespace on the far end of the trunk veth.
//
// An explicit path or name only short-circuits the discovery scan; it is
// accepted solely when the namespace behind it is the one the host-side trunk
// veth peers into. The NetConf travels in a NetworkAttachmentDefinition that a
// tenant may control, while the plugin runs as host root: without this check a
// NAD could redirect the CRA-side veth into the host netns (/proc/1/ns/net) or
// another workload's netns and bring it up there.
func resolveCRANetnsPath(conf *NetConf) (string, error) {
	spec := strings.TrimSpace(conf.CRANetns)
	trunk := conf.trunkInterface()

	var path string
	switch {
	case spec == "" || spec == "auto":
		return discoverCRANetnsByTrunk(trunk)
	case filepath.IsAbs(spec):
		if _, err := os.Stat(spec); err != nil {
			return "", fmt.Errorf("CRA netns path %q not accessible: %w", spec, err)
		}
		path = spec
	default:
		// A named netns is a single path component: anything else ("..", a
		// slash) would let a NetConf redirect the CRA-side veth into an arbitrary
		// namespace reachable from the netns run directory.
		if spec == "." || spec == ".." || strings.ContainsRune(spec, '/') {
			return "", fmt.Errorf("named CRA netns %q must be a plain name under %s", spec, netnsRunDir)
		}
		path = filepath.Join(netnsRunDir, spec)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("named CRA netns %q not found at %s: %w", spec, path, err)
		}
	}
	if err := verifyCRANetns(path, trunk); err != nil {
		return "", err
	}
	return path, nil
}

// verifyCRANetns checks that the namespace at path owns the peer of the
// host-side trunk veth, using the same kernel facts as auto-discovery.
func verifyCRANetns(path, trunk string) error {
	handle, err := netns.GetFromPath(path)
	if err != nil {
		return fmt.Errorf("failed to open CRA netns %q: %w", path, err)
	}
	defer handle.Close()
	return verifyCRANetnsHandle(handle, path, trunk)
}

// verifyCRANetnsHandle is verifyCRANetns for an already open namespace. The
// check binds to the handle, not the path it was opened from: a discovered
// /proc/<pid>/ns/net path can be re-pointed at another namespace when that
// process exits and its pid is reused, so the handle a veth is about to be
// moved through has to be verified itself, right before the move.
func verifyCRANetnsHandle(handle netns.NsHandle, path, trunk string) error {
	peer, err := trunkPeer(trunk)
	if err != nil {
		return fmt.Errorf("cannot verify CRA netns %q: %w", path, err)
	}
	if !isTrunkPeerNetns(handle, trunk, peer) {
		return fmt.Errorf("CRA netns %q does not own the peer of trunk veth %q; refusing to move the workload port there", path, trunk)
	}
	return nil
}

// discoverCRANetnsByTrunk locates the CRA network namespace through the trunk
// veth (e.g. "hbn") that connects it to the host: the node-side end lives in the
// namespace the plugin runs in, its peer in the CRA namespace. Rather than
// trusting an interface *name* found in some namespace — any pod can carry an
// interface named "hbn" (Multus lets the annotation pick the name), and the
// plugin would then move another workload's CRA-side port into that pod — the
// CRA netns is identified by kernel facts about the host-side veth that no
// other namespace can forge: the peer's netns id (IFLA_LINK_NETNSID) and the
// peer's ifindex (IFLA_LINK). A candidate namespace is the CRA netns exactly
// when the kernel reports that id for it and it owns the peer under the trunk
// name.
//
// Candidates are searched, in order, among
//
//  1. named network namespaces under /var/run/netns/ (iproute2 convention);
//  2. the network namespaces of running processes under /proc/<pid>/ns/net.
//
// The CRA (FRR / VSR) typically runs as a long-lived process whose netns is not
// bind-mounted under /var/run/netns, so the /proc scan is required to find it.
// This mirrors the CRA-VSR findWorkNSName heuristic (locate the netns by its
// trunk interface) and lets a single base-config value drive both flavors.
func discoverCRANetnsByTrunk(trunk string) (string, error) {
	peer, err := trunkPeer(trunk)
	if err != nil {
		return "", fmt.Errorf("failed to auto-discover CRA netns: %w", err)
	}

	if path, ok := searchNamedNetnsByTrunk(trunk, peer); ok {
		return path, nil
	}
	if path, ok := searchProcNetnsByTrunk(trunk, peer); ok {
		return path, nil
	}
	return "", fmt.Errorf("failed to auto-discover CRA netns: no namespace or process owns the peer of trunk veth %q", trunk)
}

// trunkPeerIdentity pins down the CRA-side end of the trunk veth as seen from
// the host: the kernel-assigned id of the namespace it lives in (relative to
// the host netns) and its ifindex in that namespace.
type trunkPeerIdentity struct {
	netnsID int
	index   int
}

// trunkPeer reads the peer identity off the host-side trunk veth. Only a veth
// has a peer to follow; any other link type (or a peer that still sits in the
// host netns) cannot identify the CRA netns, and without that identity no
// craNetns value — discovered or explicit — can be trusted.
func trunkPeer(trunk string) (trunkPeerIdentity, error) {
	link, err := netlink.LinkByName(trunk)
	if err != nil {
		return trunkPeerIdentity{}, fmt.Errorf("trunk %q not found in the host netns: %w", trunk, err)
	}
	if _, ok := link.(*netlink.Veth); !ok {
		return trunkPeerIdentity{}, fmt.Errorf("trunk %q is a %s, not a veth into the CRA netns", trunk, link.Type())
	}
	attrs := link.Attrs()
	if attrs.NetNsID < 0 || attrs.ParentIndex == 0 {
		return trunkPeerIdentity{}, fmt.Errorf("peer of trunk veth %q is not in another netns", trunk)
	}
	return trunkPeerIdentity{netnsID: attrs.NetNsID, index: attrs.ParentIndex}, nil
}

// isTrunkPeerNetns reports whether the namespace behind handle is the one the
// host-side trunk veth peers into: the kernel must report the peer's netns id
// for it and it must own the peer, under the trunk name, at the peer's ifindex.
func isTrunkPeerNetns(handle netns.NsHandle, trunk string, peer trunkPeerIdentity) bool {
	id, err := netlink.GetNetNsIdByFd(int(handle))
	if err != nil || id != peer.netnsID {
		return false
	}
	nlh, err := netlink.NewHandleAt(handle)
	if err != nil {
		return false
	}
	defer nlh.Close()

	link, err := nlh.LinkByName(trunk)
	if err != nil {
		return false
	}
	_, isVeth := link.(*netlink.Veth)
	return isVeth && link.Attrs().Index == peer.index
}

// searchNamedNetnsByTrunk scans /var/run/netns for the namespace that owns the
// trunk peer and returns its path.
func searchNamedNetnsByTrunk(trunk string, peer trunkPeerIdentity) (string, bool) {
	entries, err := os.ReadDir(netnsRunDir)
	if err != nil {
		return "", false
	}

	for _, e := range entries {
		name := e.Name()
		handle, err := netns.GetFromName(name)
		if err != nil {
			continue
		}
		found := isTrunkPeerNetns(handle, trunk, peer)
		handle.Close()
		if found {
			return filepath.Join(netnsRunDir, name), true
		}
	}
	return "", false
}

// searchProcNetnsByTrunk scans the network namespaces of running processes
// (/proc/<pid>/ns/net) for the one that owns the trunk peer and returns the
// /proc path to that namespace. Namespaces are de-duplicated by their inode so
// each distinct netns is probed at most once.
func searchProcNetnsByTrunk(trunk string, peer trunkPeerIdentity) (string, bool) {
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
		if _, dup := seen[st.Ino]; dup {
			continue
		}
		seen[st.Ino] = struct{}{}

		handle, err := netns.GetFromPath(nsPath)
		if err != nil {
			continue
		}
		found := isTrunkPeerNetns(handle, trunk, peer)
		handle.Close()
		if found {
			return nsPath, true
		}
	}
	return "", false
}
