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

// Package routedcni implements the node-local channel between the routed CNI
// plugin and the CRA agent: a gRPC service the plugin calls on ADD/DEL, backed
// by the aggregate per-node NodeRoutedPorts object as the durable source of
// truth, plus the merge that injects those ports into the NodeNetworkConfig the
// agent renders.
package routedcni

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

// DefaultSocketPath is the unix socket the CRA agent listens on and the routed
// CNI plugin dials. It lives on a hostPath shared between the two.
const DefaultSocketPath = "/run/das-schiff/routed-cni.sock"

// isDefaultVRF reports whether name denotes the underlay/default table (no
// tenant VRF): empty, "default" or "main".
func isDefaultVRF(name string) bool {
	switch strings.ToLower(name) {
	case "", "default", "main":
		return true
	default:
		return false
	}
}

// UpsertEntry inserts or replaces the entry keyed by (ContainerID, Interface).
func UpsertEntry(spec *v1alpha1.NodeRoutedPortsSpec, entry v1alpha1.RoutedPortEntry) {
	for i := range spec.Ports {
		if spec.Ports[i].ContainerID == entry.ContainerID && spec.Ports[i].Interface == entry.Interface {
			spec.Ports[i] = entry
			return
		}
	}
	spec.Ports = append(spec.Ports, entry)
}

// RemoveEntry removes entries matching containerID (and ifname when non-empty).
// It returns true if anything was removed.
func RemoveEntry(spec *v1alpha1.NodeRoutedPortsSpec, containerID, ifname string) bool {
	out := spec.Ports[:0]
	removed := false
	for _, p := range spec.Ports {
		if p.ContainerID == containerID && (ifname == "" || p.Interface == ifname) {
			removed = true
			continue
		}
		out = append(out, p)
	}
	spec.Ports = out
	return removed
}

// MergeIntoNodeNetworkConfig injects the routed-port entries into the matching
// VRF of the NodeNetworkConfig so the CRA renderer emits the infra interface and
// interface-static routes. Entries are placed by their target VRF:
//   - empty/"default"/"main" -> the cluster VRF (underlay);
//   - a name matching a fabric VRF -> that fabric VRF;
//   - any other name -> a local VRF (created if absent).
//
// It returns true if the config was changed.
func MergeIntoNodeNetworkConfig(cfg *v1alpha1.NodeNetworkConfig, entries []v1alpha1.RoutedPortEntry) bool {
	changed := false
	for i := range entries {
		if applyEntryToVRF(&cfg.Spec, entries[i]) {
			changed = true
		}
	}
	return changed
}

func applyEntryToVRF(spec *v1alpha1.NodeNetworkConfigSpec, e v1alpha1.RoutedPortEntry) bool {
	if isDefaultVRF(e.VRF) {
		if spec.ClusterVRF == nil {
			spec.ClusterVRF = &v1alpha1.VRF{}
		}
		spec.ClusterVRF.RoutedPorts = append(spec.ClusterVRF.RoutedPorts, e.RoutedPort)
		return true
	}

	if fv, ok := spec.FabricVRFs[e.VRF]; ok {
		fv.RoutedPorts = append(fv.RoutedPorts, e.RoutedPort)
		spec.FabricVRFs[e.VRF] = fv
		return true
	}

	if spec.LocalVRFs == nil {
		spec.LocalVRFs = map[string]v1alpha1.VRF{}
	}
	lv := spec.LocalVRFs[e.VRF]
	lv.RoutedPorts = append(lv.RoutedPorts, e.RoutedPort)
	spec.LocalVRFs[e.VRF] = lv
	return true
}

// HashEntries returns a stable content hash of the routed-port entries. It is
// used to detect routed-port changes that do not bump the NodeNetworkConfig
// revision, so the agent knows to re-render even on the revision fast path.
func HashEntries(entries []v1alpha1.RoutedPortEntry) string {
	b, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NodeSource reads the routed-port attachments recorded for a node from its
// aggregate NodeRoutedPorts object. It implements the agent-side source used to
// merge routed ports into the NodeNetworkConfig before rendering.
type NodeSource struct {
	client   client.Client
	nodeName string
}

// NewNodeSource builds a NodeSource for the given node.
func NewNodeSource(c client.Client, nodeName string) *NodeSource {
	return &NodeSource{client: c, nodeName: nodeName}
}

// RoutedPorts returns the routed-port entries recorded for the node, or nil if
// none have been recorded yet.
func (s *NodeSource) RoutedPorts(ctx context.Context) ([]v1alpha1.RoutedPortEntry, error) {
	nrp := &v1alpha1.NodeRoutedPorts{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: s.nodeName}, nrp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return nrp.Spec.Ports, nil
}
