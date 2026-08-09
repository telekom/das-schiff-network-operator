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

	"github.com/go-logr/logr"
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

// DefaultPortMTU is the MTU an attachment gets when its CNI configuration does
// not request one. It is shared with the plugin so both ends of the wire agree
// on what an unset mtu means.
const DefaultPortMTU = 1500

// tapIfaceSuffix distinguishes a DPDK tap's kernel netdev from the datapath
// interface it belongs to. CRA-side interface names are validated identifiers
// that never contain an underscore, so this cannot collide with one.
const tapIfaceSuffix = "_dp"

// TapIfaceName returns the kernel netdev name of the DPDK tap backing a port,
// as used by the grout renderer (pkg/cra-grout) and reported to the grout CNI
// plugin so it knows which netdev to move into the workload netns.
//
// It must never equal the interface name itself. grout creates a control plane
// representor tap named after the interface, so giving the DPDK tap that same
// name makes the representor's TUNSETIFF fail with EINVAL. grout logs the
// failure but keeps the port, then dereferences the unusable control plane fd
// on the first punted packet and dies -- taking the node's whole datapath with
// it. Names are bounded by maxInterfaceNameLen, which leaves room for the
// suffix.
func TapIfaceName(ifName string) string {
	return ifName + tapIfaceSuffix
}

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
// L2 attach entries are instead enslaved to the Layer2 domain(s) they
// reference: an access entry (Layer2AttachmentRef) becomes an untagged bridge
// slave of the matching Layer2, while a trunk entry (Layer2Trunk) becomes one
// tagged member per referenced Layer2. A trunk member without an explicit VLAN
// id inherits the id of the domain it references; an explicit id translates
// between the workload-side and the fabric-side id.
//
// An L2 entry is applied all-or-nothing: if any referenced Layer2 is not (yet)
// present on the node, or two trunk members end up on the same workload-side
// VLAN id, the whole entry is dropped and logged rather than leaving a
// half-wired trunk. The bridges are a precondition owned by the L2A pipeline,
// so a dropped entry is applied by a later reconcile once they exist.
//
// Any workload ports already present on cfg are dropped first, so merging is
// idempotent and repeated merges onto the same object cannot accumulate
// duplicates. It returns true if the config carries workload ports afterwards,
// which is not the same as len(entries) > 0: an L2 entry whose Layer2 is absent
// is dropped and does not count.
func MergeIntoNodeNetworkConfig(cfg *v1alpha1.NodeNetworkConfig, entries []v1alpha1.WorkloadPortEntry,
	log logr.Logger,
) bool {
	clearWorkloadPorts(&cfg.Spec)
	applied := false
	for i := range entries {
		e := &entries[i]
		if e.Layer2AttachmentRef != nil || len(e.Layer2Trunk) > 0 {
			if err := applyEntryToLayer2(&cfg.Spec, e); err != nil {
				log.Info("skipping L2 workload port attachment", "container", e.ContainerID,
					"interface", e.Interface, "reason", err.Error())
				continue
			}
			applied = true
			continue
		}
		applyEntryToVRF(&cfg.Spec, e)
		applied = true
	}
	return applied
}

// resolvedMember is a trunk (or access) member whose Layer2Attachment reference
// has been resolved to a Layer2 of this node's config.
type resolvedMember struct {
	// layer2 is the key of the Layer2 in spec.Layer2s the port is attached to.
	layer2 string
	// vlan is the workload-side 802.1Q id, or 0 for the untagged access member.
	vlan uint16
}

// applyEntryToLayer2 attaches an L2 entry to the Layer2 domain(s) it
// references. It resolves every member first and only mutates the spec once all
// of them are resolvable and collision-free, so a partially resolvable trunk
// never reaches the datapath.
func applyEntryToLayer2(spec *v1alpha1.NodeNetworkConfigSpec, e *v1alpha1.WorkloadPortEntry) error {
	members, err := resolveLayer2Members(spec, e)
	if err != nil {
		return err
	}
	if err := checkLayer2MTU(spec, e, members); err != nil {
		return err
	}
	for _, m := range members {
		l2 := spec.Layer2s[m.layer2]
		l2.AttachedPorts = append(l2.AttachedPorts, v1alpha1.AttachedPort{
			Interface:  e.Interface,
			PortWiring: *e.PortWiring.DeepCopy(),
			VLAN:       m.vlan,
			MTU:        e.MTU,
		})
		spec.Layer2s[m.layer2] = l2
	}
	return nil
}

// checkLayer2MTU rejects an attachment asking for more than its L2 domain can
// carry: the workload would black-hole anything above the bridge's MTU, and on
// the FRR flavor the kernel refuses a VLAN sub-interface larger than its parent
// outright.
//
// An access port has exactly one domain, so that domain has to carry the whole
// requested MTU. A trunk only needs one member of that size — the port MTU is
// sized for its largest domain, and the smaller ones are simply used below it.
// Routed attachments are not constrained at all: they never touch a bridge.
func checkLayer2MTU(spec *v1alpha1.NodeNetworkConfigSpec, e *v1alpha1.WorkloadPortEntry,
	members []resolvedMember,
) error {
	mtu := e.MTU
	if mtu == 0 {
		mtu = DefaultPortMTU
	}
	if len(members) == 0 {
		return nil
	}
	largest := members[0]
	for _, m := range members {
		// A domain that does not state an MTU constrains nothing.
		if l2MTU := spec.Layer2s[m.layer2].MTU; l2MTU == 0 || l2MTU >= mtu {
			return nil
		}
		if spec.Layer2s[m.layer2].MTU > spec.Layer2s[largest.layer2].MTU {
			largest = m
		}
	}
	if len(members) == 1 {
		return fmt.Errorf("requested mtu %d exceeds the mtu %d of Layer2 %s",
			mtu, spec.Layer2s[largest.layer2].MTU, largest.layer2)
	}
	return fmt.Errorf("requested mtu %d exceeds the mtu of every trunk member, largest is %d on Layer2 %s",
		mtu, spec.Layer2s[largest.layer2].MTU, largest.layer2)
}

// resolveLayer2Members maps every Layer2Attachment reference of an L2 entry to a
// Layer2 of this node's config and pins down the workload-side VLAN id of each
// member, inheriting the domain's own id where none was requested.
func resolveLayer2Members(spec *v1alpha1.NodeNetworkConfigSpec,
	e *v1alpha1.WorkloadPortEntry,
) ([]resolvedMember, error) {
	if e.Layer2AttachmentRef != nil {
		name, ok := findLayer2(spec, e.Layer2AttachmentRef)
		if !ok {
			return nil, fmt.Errorf("Layer2Attachment %s is not configured on this node",
				layer2AttachmentRefLog(e.Layer2AttachmentRef))
		}
		return []resolvedMember{{layer2: name}}, nil
	}

	members := make([]resolvedMember, 0, len(e.Layer2Trunk))
	seenVLANs := make(map[uint16]string, len(e.Layer2Trunk))
	seenL2s := make(map[string]string, len(e.Layer2Trunk))
	for i := range e.Layer2Trunk {
		ref := &e.Layer2Trunk[i].Layer2AttachmentRef
		name, ok := findLayer2(spec, ref)
		if !ok {
			return nil, fmt.Errorf("Layer2Attachment %s is not configured on this node",
				layer2AttachmentRefLog(ref))
		}
		// The gRPC server already rejects a repeated reference, but a
		// NodeWorkloadPorts written by hand does not go through it: carrying one
		// domain under two tags would flood every frame straight back out of the
		// port it came from.
		if other, dup := seenL2s[name]; dup {
			return nil, fmt.Errorf("trunk members %s and %s reference the same Layer2Attachment",
				other, layer2AttachmentRefLog(ref))
		}
		seenL2s[name] = layer2AttachmentRefLog(ref)
		// No explicit id means the domain is carried under its own VLAN id, which
		// only the node config knows; an explicit one translates it.
		vlan := spec.Layer2s[name].VLAN
		if requested := e.Layer2Trunk[i].VLAN; requested != nil {
			vlan = *requested
		}
		if vlan == 0 {
			return nil, fmt.Errorf("Layer2Attachment %s has no VLAN id to inherit as a trunk member",
				layer2AttachmentRefLog(ref))
		}
		// An explicit id was already bounded on the way in, but an inherited one
		// comes from the Layer2 schema, which still allows the reserved 4095/4096.
		if vlan > maxVLANID {
			return nil, fmt.Errorf("Layer2Attachment %s carries vlan id %d, which is not assignable to a trunk member",
				layer2AttachmentRefLog(ref), vlan)
		}
		if other, dup := seenVLANs[vlan]; dup {
			return nil, fmt.Errorf("trunk members %s and %s collide on workload-side vlan %d",
				other, layer2AttachmentRefLog(ref), vlan)
		}
		seenVLANs[vlan] = layer2AttachmentRefLog(ref)
		members = append(members, resolvedMember{layer2: name, vlan: vlan})
	}
	return members, nil
}

// findLayer2 returns the key of the Layer2 whose stamped AttachmentRef matches
// ref.
func findLayer2(spec *v1alpha1.NodeNetworkConfigSpec, ref *v1alpha1.Layer2AttachmentRef) (string, bool) {
	for name, l2 := range spec.Layer2s {
		if layer2AttachmentRefEqual(l2.AttachmentRef, ref) {
			return name, true
		}
	}
	return "", false
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

// vlanSortKey orders a trunk member's workload-side VLAN id, with "inherited"
// (unset) sorting before every explicit id so the ordering is total.
func vlanSortKey(vlan *uint16) int {
	if vlan == nil {
		return -1
	}
	return int(*vlan)
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
		// The trunk is a set: the render order of its members does not matter,
		// but their VLAN ids do, so sort by reference and keep the ids.
		if len(normalised[i].Layer2Trunk) == 0 {
			normalised[i].Layer2Trunk = []v1alpha1.Layer2TrunkMember{}
		} else {
			slices.SortFunc(normalised[i].Layer2Trunk, func(a, b v1alpha1.Layer2TrunkMember) int {
				return cmp.Or(
					strings.Compare(a.Namespace, b.Namespace),
					strings.Compare(a.Name, b.Name),
					// A well-formed trunk never references one attachment twice,
					// but a hand-written one can, and the hash must still be a
					// function of the set rather than of its order.
					cmp.Compare(vlanSortKey(a.VLAN), vlanSortKey(b.VLAN)),
				)
			})
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
