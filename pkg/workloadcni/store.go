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

// Package workloadcni implements the node-local channel between the workload CNI
// plugin and the CRA agent: a gRPC service the plugin calls on ADD/DEL, backed
// by the aggregate per-node NodeWorkloadPorts object as the durable source of
// truth, plus the merge that injects those ports into the NodeNetworkConfig the
// agent renders.
package workloadcni

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

// DefaultSocketPath is the unix socket the CRA agent listens on and the routed
// CNI plugin dials. It lives on a hostPath shared between the two.
const DefaultSocketPath = "/run/das-schiff/workload-cni.sock"

// InfraPortPrefix is prepended to a routed CRA-side interface name to form the
// 6WIND VSR infrastructure "port" reference. 6WIND resolves that port to a
// kernel interface by its ifalias (not its devname), so this value must be set
// as the moved interface's alias by the workload CNI (see pkg/cni) and emitted as
// the infrastructure port by the VSR renderer (see pkg/cra-vsr). If the alias is
// missing the VSR cannot bind the port and reaps the veth, which also destroys
// its pod-side peer. The two sides therefore MUST use this same prefix.
const InfraPortPrefix = "infra-"

// FpvhostPortPrefix is prepended to a CRA-side interface name to form the 6WIND
// VSR fast-path fpvhost virtual-port reference (fpvhost-<ifname>) used by the
// vhost-user transport. Unlike InfraPortPrefix there is no netns-moved veth to
// alias: the virtual-port is declared by the VSR renderer itself under
// "system fast-path virtual-port fpvhost". The fast path does materialise it as
// an interface, though, so the reference is bounded by the same kernel
// interface-name limit and, being two characters longer than InfraPortPrefix,
// leaves a vhost-user port name that much less room (see the CNI plugin's
// vhostPortName and maxVhostInterfaceNameLen above).
const FpvhostPortPrefix = "fpvhost-"

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
// It returns true if the spec was changed; a repeated ADD carrying an identical
// entry is a no-op so callers can skip a needless API write.
func UpsertEntry(spec *v1alpha1.NodeWorkloadPortsSpec, entry *v1alpha1.WorkloadPortEntry) bool {
	for i := range spec.Ports {
		if spec.Ports[i].ContainerID == entry.ContainerID && spec.Ports[i].Interface == entry.Interface {
			if equality.Semantic.DeepEqual(&spec.Ports[i], entry) {
				return false
			}
			spec.Ports[i] = *entry.DeepCopy()
			return true
		}
	}
	spec.Ports = append(spec.Ports, *entry.DeepCopy())
	return true
}

// RemoveEntry removes entries matching containerID (and ifname when non-empty).
// It returns true if anything was removed.
func RemoveEntry(spec *v1alpha1.NodeWorkloadPortsSpec, containerID, ifname string) bool {
	out := spec.Ports[:0]
	removed := false
	for i := range spec.Ports {
		p := &spec.Ports[i]
		if p.ContainerID == containerID && (ifname == "" || p.Interface == ifname) {
			removed = true
			continue
		}
		out = append(out, *p)
	}
	spec.Ports = out
	return removed
}

// MergeIntoNodeNetworkConfig injects the workload-port entries into the matching
// VRF of the NodeNetworkConfig so the CRA renderer emits the infra interface and
// interface-static routes. Entries are placed by their target VRF:
//   - empty/"default"/"main" -> the node's default (no-l3vrf) table via
//     spec.GlobalWorkloadPorts;
//   - a name matching a fabric VRF -> that fabric VRF;
//   - any other name -> a local VRF (created if absent).
//
// The cluster and management VRFs are owned by the platform and are not
// workload-port targets: their names are only known to the CRA base config, not
// here, so a workload port must never be addressed to them.
//
// L2 attach entries (those carrying a Layer2AttachmentRef) are instead enslaved
// to the Layer2 whose AttachmentRef matches, as bridge slaves with no L3
// addressing. An L2 entry whose referenced Layer2 is not (yet) present on the
// node is skipped: the bridge is a precondition owned by the L2A pipeline.
//
// Any workload ports already present on cfg are dropped first, so merging is
// idempotent and repeated merges onto the same object cannot accumulate
// duplicates. It returns true if the config carries workload ports afterwards,
// which is not the same as len(entries) > 0: an L2 entry whose Layer2 is absent
// is dropped and does not count.
func MergeIntoNodeNetworkConfig(cfg *v1alpha1.NodeNetworkConfig, entries []v1alpha1.WorkloadPortEntry) bool {
	clearWorkloadPorts(&cfg.Spec)
	applied := false
	for i := range entries {
		if entries[i].Layer2AttachmentRef != nil {
			applied = applyEntryToLayer2(&cfg.Spec, &entries[i]) || applied
			continue
		}
		applyEntryToVRF(&cfg.Spec, &entries[i])
		applied = true
	}
	return applied
}

// applyEntryToLayer2 enslaves an L2 attach entry to the Layer2 whose
// AttachmentRef matches the entry's Layer2AttachmentRef, as a bridge slave. If
// no such Layer2 is present on the node the entry is dropped (assume-exists)
// and false is returned.
func applyEntryToLayer2(spec *v1alpha1.NodeNetworkConfigSpec, e *v1alpha1.WorkloadPortEntry) bool {
	for name, l2 := range spec.Layer2s {
		if !layer2AttachmentRefEqual(l2.AttachmentRef, e.Layer2AttachmentRef) {
			continue
		}
		l2.AttachedPorts = append(l2.AttachedPorts, v1alpha1.AttachedPort{
			Interface:  e.Interface,
			PortWiring: e.PortWiring,
		})
		spec.Layer2s[name] = l2
		return true
	}
	return false
}

// layer2AttachmentRefEqual reports whether two Layer2AttachmentRefs denote the
// same Layer2Attachment. Nil refs never match.
func layer2AttachmentRefEqual(a, b *v1alpha1.Layer2AttachmentRef) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name && a.Namespace == b.Namespace
}

// clearWorkloadPorts drops every workload port previously merged into spec
// (both routed VRF placements and L2 bridge-slave attachments).
// It deliberately mirrors applyEntryToVRF: spec.ClusterVRF is not touched
// because it is never a placement target, so it can never hold merged ports.
func clearWorkloadPorts(spec *v1alpha1.NodeNetworkConfigSpec) {
	spec.GlobalWorkloadPorts = nil
	for name := range spec.FabricVRFs {
		fv := spec.FabricVRFs[name]
		fv.WorkloadPorts = nil
		spec.FabricVRFs[name] = fv
	}
	for name := range spec.LocalVRFs {
		lv := spec.LocalVRFs[name]
		lv.WorkloadPorts = nil
		spec.LocalVRFs[name] = lv
	}
	for name := range spec.Layer2s {
		l2 := spec.Layer2s[name]
		l2.AttachedPorts = nil
		spec.Layer2s[name] = l2
	}
}

func applyEntryToVRF(spec *v1alpha1.NodeNetworkConfigSpec, e *v1alpha1.WorkloadPortEntry) {
	// Deep-copy so the merged spec never aliases the caller's entry slices.
	port := *e.WorkloadPort.DeepCopy()

	if isDefaultVRF(e.VRF) {
		spec.GlobalWorkloadPorts = append(spec.GlobalWorkloadPorts, port)
		return
	}

	if fv, ok := spec.FabricVRFs[e.VRF]; ok {
		fv.WorkloadPorts = append(fv.WorkloadPorts, port)
		spec.FabricVRFs[e.VRF] = fv
		return
	}

	if spec.LocalVRFs == nil {
		spec.LocalVRFs = map[string]v1alpha1.VRF{}
	}
	lv := spec.LocalVRFs[e.VRF]
	lv.WorkloadPorts = append(lv.WorkloadPorts, port)
	spec.LocalVRFs[e.VRF] = lv
}

// HashEntries returns a stable content hash of the workload-port entries. It is
// used to detect workload-port changes that do not bump the NodeNetworkConfig
// revision, so the agent knows to re-render even on the revision fast path.
//
// Entries are normalised (sorted by their identity key, nil/empty slices folded
// together) before hashing so that a reordered or re-serialised but otherwise
// identical set does not force a re-render.
func HashEntries(entries []v1alpha1.WorkloadPortEntry) string {
	normalised := make([]v1alpha1.WorkloadPortEntry, len(entries))
	for i := range entries {
		normalised[i] = *entries[i].DeepCopy()
		if len(normalised[i].HostRoutes) == 0 {
			normalised[i].HostRoutes = []string{}
		} else {
			slices.Sort(normalised[i].HostRoutes)
		}
	}
	slices.SortFunc(normalised, func(a, b v1alpha1.WorkloadPortEntry) int {
		return cmp.Or(
			strings.Compare(a.ContainerID, b.ContainerID),
			strings.Compare(a.Interface, b.Interface),
		)
	})

	b, err := json.Marshal(normalised)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NodeSource reads the workload-port attachments recorded for a node from its
// aggregate NodeWorkloadPorts object. It implements the agent-side source used to
// merge workload ports into the NodeNetworkConfig before rendering.
type NodeSource struct {
	client   client.Client
	nodeName string
}

// NewNodeSource builds a NodeSource for the given node.
func NewNodeSource(c client.Client, nodeName string) *NodeSource {
	return &NodeSource{client: c, nodeName: nodeName}
}

// WorkloadPorts returns the workload-port entries recorded for the node, or nil if
// none have been recorded yet.
func (s *NodeSource) WorkloadPorts(ctx context.Context) ([]v1alpha1.WorkloadPortEntry, error) {
	nrp := &v1alpha1.NodeWorkloadPorts{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: s.nodeName}, nrp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting NodeWorkloadPorts %q: %w", s.nodeName, err)
	}
	return nrp.Spec.Ports, nil
}
