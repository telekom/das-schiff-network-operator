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
	"errors"
	"fmt"
	"net"
	"regexp"
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
// the infrastructure port by the VSR renderer (follow-up #356). If the alias is
// missing the VSR cannot bind the port and reaps the veth, which also destroys
// its pod-side peer. The two sides therefore MUST use this same prefix.
const InfraPortPrefix = "infra-"

// DefaultPortMTU is the MTU an attachment gets when its CNI configuration does
// not request one. It is shared with the plugin so both ends of the wire agree
// on what an unset mtu means.
const DefaultPortMTU = 1500

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

// safeNetName matches the interface and VRF names the entries may carry. Both
// are rendered verbatim into the CRA configuration (FRR `vrf <name>`, `router
// bgp ... vrf <name>`, route-map names), so anything beyond a plain kernel
// device name — whitespace, quotes, a newline — could break or extend the
// generated configuration.
var safeNetName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ValidateEntry checks a workload-port entry against the rules an ADD has to
// satisfy: the fields NodeWorkloadPorts requires, an interface name the kernel
// accepts, a VRF name that is a plain device name and not platform-owned, and
// gateways/host routes that are single-address prefixes of their own family.
// The same check guards both the gRPC entry point and the durable entries read
// back before the merge, since the CRD schema alone accepts broader prefixes
// that would advertise more than one workload host.
func ValidateEntry(e *v1alpha1.WorkloadPortEntry, reservedVRFs map[string]bool) error {
	if e.Interface == "" {
		return errors.New("interface is required")
	}
	if len(e.Interface) > maxInterfaceNameLen {
		return fmt.Errorf("interface %q exceeds %d characters", e.Interface, maxInterfaceNameLen)
	}
	if !safeNetName.MatchString(e.Interface) {
		return fmt.Errorf("interface %q is not a plain device name", e.Interface)
	}
	if e.VRF != "" {
		if len(e.VRF) > kernelIfNameLen {
			return fmt.Errorf("vrf %q exceeds %d characters", e.VRF, kernelIfNameLen)
		}
		if !safeNetName.MatchString(e.VRF) {
			return fmt.Errorf("vrf %q is not a plain device name", e.VRF)
		}
	}
	if reservedVRFs[e.VRF] {
		return fmt.Errorf("vrf %q is platform-owned and not a workload-port target", e.VRF)
	}
	if e.ContainerID == "" {
		return errors.New("container_id is required")
	}
	if e.PodNamespace == "" {
		return errors.New("pod_namespace is required")
	}
	if e.PodName == "" {
		return errors.New("pod_name is required")
	}
	if err := validateGateway("gateway_v4", e.GatewayV4, true); err != nil {
		return err
	}
	if err := validateGateway("gateway_v6", e.GatewayV6, false); err != nil {
		return err
	}
	for _, hr := range e.HostRoutes {
		if err := validateHostRoute(hr); err != nil {
			return err
		}
	}
	if e.MTU != 0 && (e.MTU < MinPortMTU || e.MTU > MaxPortMTU) {
		return fmt.Errorf("mtu %d is out of range (%d-%d)", e.MTU, MinPortMTU, MaxPortMTU)
	}
	return validateLayer2Attach(e)
}

// validateLayer2Attach enforces the mutual exclusion between L2 attach mode and
// the routed fields, and between the access and trunk forms of L2 attach, and
// checks each trunk member for a usable reference, VLAN id and sub-interface
// name. Members that inherit their VLAN id can only be checked for collisions
// once the referenced Layer2 is known, which happens at merge time.
func validateLayer2Attach(e *v1alpha1.WorkloadPortEntry) error {
	ref, trunk := e.Layer2AttachmentRef, e.Layer2Trunk
	if ref == nil && len(trunk) == 0 {
		return nil
	}
	if ref != nil && len(trunk) > 0 {
		return errors.New("layer2_attachment_ref (untagged access port) and layer2_trunk (tagged trunk) are mutually exclusive")
	}
	if ref != nil && ref.Name == "" {
		return errors.New("layer2_attachment_ref.name is required")
	}
	if e.VRF != "" || e.GatewayV4 != "" || e.GatewayV6 != "" || len(e.HostRoutes) > 0 {
		return errors.New("L2 attach mode is mutually exclusive with vrf, gateways and host routes")
	}
	if len(trunk) > maxLayer2TrunkMembers {
		return fmt.Errorf("layer2_trunk has %d members (max %d)", len(trunk), maxLayer2TrunkMembers)
	}
	seenRefs := make(map[string]struct{}, len(trunk))
	seenVLANs := make(map[uint16]struct{}, len(trunk))
	for i := range trunk {
		if err := validateLayer2TrunkMember(e.Interface, &trunk[i], seenRefs, seenVLANs); err != nil {
			return err
		}
	}
	return nil
}

// validateLayer2TrunkMember checks one trunk member: a usable reference that is
// not listed twice, a valid and unique explicit VLAN id, and a sub-interface
// name the kernel accepts. Inherited VLANs are not known until the merge, so
// they reserve the largest assignable suffix; explicit VLANs use their actual
// length.
func validateLayer2TrunkMember(iface string, m *v1alpha1.Layer2TrunkMember,
	seenRefs map[string]struct{}, seenVLANs map[uint16]struct{},
) error {
	if m.Name == "" {
		return errors.New("layer2_trunk member requires ref.name")
	}
	key := layer2AttachmentRefLog(&m.Layer2AttachmentRef)
	if _, dup := seenRefs[key]; dup {
		return fmt.Errorf("layer2_trunk references %q more than once", m.Name)
	}
	seenRefs[key] = struct{}{}
	vlan := uint16(maxVLANID)
	if m.VLAN != nil {
		vlan = *m.VLAN
		if vlan == 0 || vlan > maxVLANID {
			return fmt.Errorf("layer2_trunk member %q has an invalid vlan %d (want 1-%d)", m.Name, vlan, maxVLANID)
		}
		if _, dup := seenVLANs[vlan]; dup {
			return fmt.Errorf("layer2_trunk uses vlan %d more than once", vlan)
		}
		seenVLANs[vlan] = struct{}{}
	}
	if sub := fmt.Sprintf("%s.%d", iface, vlan); len(sub) > kernelIfNameLen {
		return fmt.Errorf("layer2_trunk member %q creates sub-interface %q exceeding %d characters",
			m.Name, sub, kernelIfNameLen)
	}
	return nil
}

// validateGateway checks an optional on-link gateway address: it must be a CIDR
// of the field's own address family, since it is rendered into that family's
// datapath configuration and a mismatch would silently land in the wrong one.
func validateGateway(field, value string, wantV4 bool) error {
	if value == "" {
		return nil
	}
	ip, ipNet, err := net.ParseCIDR(value)
	if err != nil {
		return fmt.Errorf("invalid %s %q", field, value)
	}
	if isV4 := ip.To4() != nil; isV4 != wantV4 {
		family := "IPv6"
		if wantV4 {
			family = "IPv4"
		}
		return fmt.Errorf("%s %q is not an %s address", field, value, family)
	}
	// The gateway is added verbatim as an address on the CRA-side port, so a
	// shorter prefix would put a whole connected subnet on that interface and
	// leak it into the fabric. Only a single-address prefix is meaningful for
	// an on-link gateway.
	if ones, bits := ipNet.Mask.Size(); ones != bits {
		return fmt.Errorf("%s %q must be a single address (/%d)", field, value, bits)
	}
	// Likewise only a link-local address may be claimed on the port: the
	// attachment config is not trusted to hand the CRA an underlay or node
	// address as its "gateway".
	if !ip.IsLinkLocalUnicast() {
		return fmt.Errorf("%s %q is not a link-local address", field, value)
	}
	return nil
}

// validateHostRoute checks that a workload host route is a single-address
// prefix (/32 or /128). A shorter prefix would advertise a whole subnet toward
// the fabric via the workload's port, which is never intended.
func validateHostRoute(value string) error {
	ip, ipNet, err := net.ParseCIDR(value)
	if err != nil {
		return fmt.Errorf("invalid host route %q", value)
	}
	ones, bits := ipNet.Mask.Size()
	if ones != bits {
		return fmt.Errorf("host route %q must be a single address (/%d)", value, bits)
	}
	if (ip.To4() != nil) != (bits == net.IPv4len*8) {
		return fmt.Errorf("host route %q has a mismatched prefix length", value)
	}
	return nil
}

// DropInvalidEntries returns the entries that pass ValidateEntry, logging the
// ones it drops. The server already refuses such an ADD; this guards the
// datapath against an entry that reached NodeWorkloadPorts some other way (an
// older agent, a hand-written object) and would otherwise be programmed as
// recorded — a /24 host route advertised to the fabric, a local VRF adopting
// the platform's device of that name, or an L2 trunk colliding with itself.
func DropInvalidEntries(entries []v1alpha1.WorkloadPortEntry, reservedVRFs []string,
	log logr.Logger,
) []v1alpha1.WorkloadPortEntry {
	reserved := make(map[string]bool, len(reservedVRFs))
	for _, name := range reservedVRFs {
		if name != "" {
			reserved[name] = true
		}
	}
	// A port is one netdev and can only be programmed one way; two entries
	// naming the same interface (which the server never records, since it keys
	// on container and interface) would be applied in list order with the last
	// one winning, so all of them are dropped rather than picking one.
	byInterface := make(map[string]int, len(entries))
	for i := range entries {
		byInterface[entries[i].Interface]++
	}
	kept := make([]v1alpha1.WorkloadPortEntry, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		if byInterface[e.Interface] > 1 {
			log.Info("dropping workload port entry whose interface is claimed more than once",
				"container", e.ContainerID, "interface", e.Interface)
			continue
		}
		if err := ValidateEntry(e, reserved); err != nil {
			log.Info("dropping invalid workload port entry", "container", e.ContainerID,
				"interface", e.Interface, "reason", err.Error())
			continue
		}
		kept = append(kept, *e)
	}
	return kept
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
// here, so the agent refuses them at ADD (WithReservedVRFs) and drops any entry
// that still names one before merging (DropInvalidEntries).
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
			Interface: e.Interface,
			VLAN:      m.vlan,
			MTU:       e.MTU,
		})
		spec.Layer2s[m.layer2] = l2
	}
	return nil
}

// checkLayer2MTU rejects an attachment asking for more than its L2 domains can
// carry: the workload would black-hole anything above a bridge's MTU, and on
// the FRR flavor the kernel refuses a VLAN sub-interface larger than its parent
// outright.
//
// Every domain the port touches has to carry the whole requested MTU. A trunk's
// sub-interfaces all inherit the port MTU, so one large member does not make
// the smaller ones safe: frames sized for the port would still be dropped on a
// member whose bridge cannot carry them, and the workload has no way to tell.
// Routed attachments are not constrained at all: they never touch a bridge.
func checkLayer2MTU(spec *v1alpha1.NodeNetworkConfigSpec, e *v1alpha1.WorkloadPortEntry,
	members []resolvedMember,
) error {
	mtu := e.MTU
	if mtu == 0 {
		mtu = DefaultPortMTU
	}
	for _, m := range members {
		// A domain that does not state an MTU constrains nothing.
		l2MTU := spec.Layer2s[m.layer2].MTU
		if l2MTU == 0 || l2MTU >= mtu {
			continue
		}
		if len(members) == 1 {
			return fmt.Errorf("requested mtu %d exceeds the mtu %d of Layer2 %s", mtu, l2MTU, m.layer2)
		}
		return fmt.Errorf("requested mtu %d exceeds the mtu %d of trunk member Layer2 %s", mtu, l2MTU, m.layer2)
	}
	return nil
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
		// Cannot happen for a struct of plain strings, but never return "": that
		// is the "no workload ports source" value and would mask the entries.
		b = fmt.Appendf(nil, "%#v", normalised)
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
