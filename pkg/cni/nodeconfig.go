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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// NodeConfigPath is the node-local file the plugin reads its trust anchors
// from. It lives next to (not inside) the kubelet's CNI directory so the
// runtime never mistakes it for a network config, and it is written by the
// installer DaemonSet from an operator-owned ConfigMap.
//
// The attachment config (the NetworkAttachmentDefinition) is tenant-writable
// in a multi-tenant cluster, so anything that decides *where* a root-run
// plugin wires the port — the agent socket it trusts, the netns it moves the
// CRA-side end into, the trunk interface that identifies that netns — must
// not come from there. These settings are node properties anyway: they are
// the same for every attachment on a node and differ only between clusters.
const NodeConfigPath = "/etc/cni/cni-workload/config.json"

// nodeConfigPath is NodeConfigPath, indirected for tests.
var nodeConfigPath = NodeConfigPath

// defaultTrunkInterface is the interface that identifies the CRA netns when the
// node config does not name one. Mirrors BaseConfig.TrunkInterfaceName.
const defaultTrunkInterface = "hbn"

// NodeConfig holds the node-level plugin settings, see NodeConfigPath. Every
// field is optional; an absent file means "all defaults".
type NodeConfig struct {
	// AgentSocket is the unix socket of the node-local CRA agent
	// (workloadcni.DefaultSocketPath when empty).
	AgentSocket string `json:"agentSocket,omitempty"`
	// CRANetns selects the CRA network namespace the CRA-side veth end is moved
	// into. Accepted values:
	//   - "" or "auto": auto-discover (see TrunkInterface / discovery.go)
	//   - "<name>":     a named netns under /var/run/netns/<name>
	//   - "/path":      an absolute netns path (e.g. /proc/<pid>/ns/net)
	// A name or path is only accepted if that namespace owns the peer of the
	// TrunkInterface veth; it cannot redirect the port elsewhere.
	CRANetns string `json:"craNetns,omitempty"`
	// TrunkInterface is the interface name that identifies the CRA network
	// namespace during auto-discovery (the netns that owns its veth peer).
	// Defaults to "hbn" when empty.
	TrunkInterface string `json:"trunkInterface,omitempty"`
}

// loadNodeConfig reads the node config at path. A missing file yields the
// zero NodeConfig (defaults). Because the file steers a root-run plugin it is
// only trusted when it is a regular, root-owned file nobody else can write:
// anything else is an error rather than a silent fallback, so a tampered or
// misinstalled file shows up as a failing ADD instead of a misdirected port.
func loadNodeConfig(path string) (*NodeConfig, error) {
	cfg := &NodeConfig{}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to stat node config %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("node config %s is not a regular file (mode %s)", path, info.Mode())
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("node config %s is group/world-writable (mode %s)", path, info.Mode().Perm())
	}
	// Root-owned in production (the plugin runs as root); owned by the caller
	// when run unprivileged, e.g. in tests.
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("node config %s is owned by uid %d, not root", path, st.Uid)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read node config %s: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return cfg, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse node config %s: %w", path, err)
	}
	return cfg, nil
}
