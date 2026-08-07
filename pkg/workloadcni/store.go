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
			spec.Ports[i] = *entry
			return true
		}
	}
	spec.Ports = append(spec.Ports, *entry)
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
// recorded — a /24 host route advertised to the fabric, or a local VRF adopting
// the platform's device of that name.
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
// Any workload ports already present on cfg are dropped first, so merging is
// idempotent and repeated merges onto the same object cannot accumulate
// duplicates. It returns true if the config carries workload ports afterwards.
func MergeIntoNodeNetworkConfig(cfg *v1alpha1.NodeNetworkConfig, entries []v1alpha1.WorkloadPortEntry) bool {
	clearWorkloadPorts(&cfg.Spec)
	for i := range entries {
		applyEntryToVRF(&cfg.Spec, &entries[i])
	}
	return len(entries) > 0
}

// clearWorkloadPorts drops every workload port previously merged into spec.
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
}

func applyEntryToVRF(spec *v1alpha1.NodeNetworkConfigSpec, e *v1alpha1.WorkloadPortEntry) {
	if isDefaultVRF(e.VRF) {
		spec.GlobalWorkloadPorts = append(spec.GlobalWorkloadPorts, e.WorkloadPort)
		return
	}

	if fv, ok := spec.FabricVRFs[e.VRF]; ok {
		fv.WorkloadPorts = append(fv.WorkloadPorts, e.WorkloadPort)
		spec.FabricVRFs[e.VRF] = fv
		return
	}

	if spec.LocalVRFs == nil {
		spec.LocalVRFs = map[string]v1alpha1.VRF{}
	}
	lv := spec.LocalVRFs[e.VRF]
	lv.WorkloadPorts = append(lv.WorkloadPorts, e.WorkloadPort)
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
