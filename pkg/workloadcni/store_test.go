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

package workloadcni

import (
	"fmt"
	"testing"

	"github.com/go-logr/logr"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

func entry(containerID, iface, vrf string) *v1alpha1.WorkloadPortEntry {
	return &v1alpha1.WorkloadPortEntry{
		PodNamespace: "ns",
		PodName:      "pod",
		ContainerID:  containerID,
		VRF:          vrf,
		WorkloadPort: v1alpha1.WorkloadPort{
			Interface:  iface,
			GatewayV4:  "169.254.1.1/32",
			GatewayV6:  "fe80::1/128",
			HostRoutes: []string{"10.201.0.10/32", "fd00:201::10/128"},
		},
	}
}

func TestUpsertEntry(t *testing.T) {
	spec := &v1alpha1.NodeWorkloadPortsSpec{}

	if !UpsertEntry(spec, entry("c1", "eth1", "")) {
		t.Fatal("expected the first insert to report a change")
	}
	if len(spec.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(spec.Ports))
	}

	// Re-adding an identical entry is a no-op, so no needless API write.
	if UpsertEntry(spec, entry("c1", "eth1", "")) {
		t.Fatal("expected an identical re-add to report no change")
	}

	// Same (containerID, interface) replaces in place.
	updated := entry("c1", "eth1", "")
	updated.HostRoutes = []string{"10.201.0.11/32"}
	if !UpsertEntry(spec, updated) {
		t.Fatal("expected a changed entry to report a change")
	}
	if len(spec.Ports) != 1 {
		t.Fatalf("expected upsert to replace in place, got %d ports", len(spec.Ports))
	}
	if got := spec.Ports[0].HostRoutes[0]; got != "10.201.0.11/32" {
		t.Fatalf("expected replaced host route, got %q", got)
	}

	// Different interface appends.
	UpsertEntry(spec, entry("c1", "eth2", ""))
	if len(spec.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(spec.Ports))
	}
}

func TestRemoveEntry(t *testing.T) {
	spec := &v1alpha1.NodeWorkloadPortsSpec{}
	UpsertEntry(spec, entry("c1", "eth1", ""))
	UpsertEntry(spec, entry("c1", "eth2", ""))
	UpsertEntry(spec, entry("c2", "eth1", ""))

	// Remove a specific interface of a container.
	if !RemoveEntry(spec, "c1", "eth1") {
		t.Fatal("expected removal to report a change")
	}
	if len(spec.Ports) != 2 {
		t.Fatalf("expected 2 ports remaining, got %d", len(spec.Ports))
	}

	// Removing an unknown attachment is a no-op.
	if RemoveEntry(spec, "c9", "eth9") {
		t.Fatal("expected no change removing unknown attachment")
	}

	// Empty interface removes all attachments of the container.
	if !RemoveEntry(spec, "c1", "") {
		t.Fatal("expected removal to report a change")
	}
	if len(spec.Ports) != 1 || spec.Ports[0].ContainerID != "c2" {
		t.Fatalf("expected only c2 remaining, got %+v", spec.Ports)
	}
}

func TestMergeIntoNodeNetworkConfig(t *testing.T) {
	cfg := &v1alpha1.NodeNetworkConfig{
		Spec: v1alpha1.NodeNetworkConfigSpec{
			FabricVRFs: map[string]v1alpha1.FabricVRF{
				"cluster": {},
			},
		},
	}

	entries := []v1alpha1.WorkloadPortEntry{
		*entry("c1", "eth1", ""),         // -> default table / GlobalWorkloadPorts
		*entry("c2", "eth2", "cluster"),  // -> fabric VRF "cluster"
		*entry("c3", "eth3", "tenant-a"), // -> new local VRF
	}

	if !MergeIntoNodeNetworkConfig(cfg, entries, logr.Discard()) {
		t.Fatal("expected merge to report a change")
	}

	if len(cfg.Spec.GlobalWorkloadPorts) != 1 {
		t.Fatalf("expected 1 global workload port, got %+v", cfg.Spec.GlobalWorkloadPorts)
	}
	if fv := cfg.Spec.FabricVRFs["cluster"]; len(fv.WorkloadPorts) != 1 {
		t.Fatalf("expected 1 workload port on fabric VRF cluster, got %+v", fv.WorkloadPorts)
	}
	if lv, ok := cfg.Spec.LocalVRFs["tenant-a"]; !ok || len(lv.WorkloadPorts) != 1 {
		t.Fatalf("expected 1 workload port on local VRF tenant-a, got %+v", cfg.Spec.LocalVRFs)
	}

	// Merging again onto the same object replaces rather than accumulates.
	MergeIntoNodeNetworkConfig(cfg, entries, logr.Discard())
	if len(cfg.Spec.GlobalWorkloadPorts) != 1 {
		t.Fatalf("expected merge to be idempotent, got %+v", cfg.Spec.GlobalWorkloadPorts)
	}
	if fv := cfg.Spec.FabricVRFs["cluster"]; len(fv.WorkloadPorts) != 1 {
		t.Fatalf("expected merge to be idempotent on fabric VRF, got %+v", fv.WorkloadPorts)
	}

	// A subsequent merge with no entries clears the previously merged ports.
	MergeIntoNodeNetworkConfig(cfg, nil, logr.Discard())
	if len(cfg.Spec.GlobalWorkloadPorts) != 0 {
		t.Fatalf("expected global workload ports to be cleared, got %+v", cfg.Spec.GlobalWorkloadPorts)
	}
	if fv := cfg.Spec.FabricVRFs["cluster"]; len(fv.WorkloadPorts) != 0 {
		t.Fatalf("expected fabric VRF workload ports to be cleared, got %+v", fv.WorkloadPorts)
	}
	if lv := cfg.Spec.LocalVRFs["tenant-a"]; len(lv.WorkloadPorts) != 0 {
		t.Fatalf("expected local VRF workload ports to be cleared, got %+v", lv.WorkloadPorts)
	}
}

// TestDropInvalidEntries ensures an entry recorded against a platform-owned VRF
// or carrying prefixes the server would refuse never reaches the merge, where
// it would become a local VRF adopting the platform's device of that name or a
// subnet route advertised to the fabric.
func TestDropInvalidEntries(t *testing.T) {
	subnet := entry("c5", "eth5", "")
	subnet.HostRoutes = []string{"10.201.0.0/24"}
	wideGW := entry("c6", "eth6", "")
	wideGW.GatewayV4 = "169.254.1.1/24"
	entries := []v1alpha1.WorkloadPortEntry{
		*entry("c1", "eth1", ""),
		*entry("c2", "eth2", "mgmt"),
		*entry("c3", "eth3", "tenant-a"),
		*entry("c4", "eth4", "cluster"),
		*subnet,
		*wideGW,
		// Two containers claiming the same port: neither may be programmed.
		*entry("c7", "eth7", "tenant-a"),
		*entry("c8", "eth7", "tenant-b"),
	}

	kept := DropInvalidEntries(entries, []string{"mgmt", "cluster", ""}, logr.Discard())
	if len(kept) != 2 || kept[0].ContainerID != "c1" || kept[1].ContainerID != "c3" {
		t.Fatalf("expected the default-table and tenant entries to survive, got %+v", kept)
	}

	// The empty reserved name must not swallow default-table entries, and no
	// reserved names at all must still drop the invalid prefixes only.
	if got := DropInvalidEntries(entries, nil, logr.Discard()); len(got) != 4 {
		t.Fatalf("expected only the invalid prefixes filtered without reserved names, got %+v", got)
	}

	cfg := &v1alpha1.NodeNetworkConfig{}
	MergeIntoNodeNetworkConfig(cfg, kept, logr.Discard())
	if _, ok := cfg.Spec.LocalVRFs["mgmt"]; ok {
		t.Fatalf("platform VRF must not be materialised as a local VRF: %+v", cfg.Spec.LocalVRFs)
	}
}

func TestMergeEmptyIsNoOp(t *testing.T) {
	cfg := &v1alpha1.NodeNetworkConfig{}
	if MergeIntoNodeNetworkConfig(cfg, nil, logr.Discard()) {
		t.Fatal("expected no change merging nil entries")
	}
}

func l2Entry(containerID, iface, l2aName, l2aNamespace string) *v1alpha1.WorkloadPortEntry {
	return &v1alpha1.WorkloadPortEntry{
		PodNamespace: "ns",
		PodName:      "pod",
		ContainerID:  containerID,
		Layer2AttachmentRef: &v1alpha1.Layer2AttachmentRef{
			Name:      l2aName,
			Namespace: l2aNamespace,
		},
		WorkloadPort: v1alpha1.WorkloadPort{Interface: iface},
	}
}

func TestMergeL2AttachEnslavesMatchingLayer2(t *testing.T) {
	cfg := &v1alpha1.NodeNetworkConfig{
		Spec: v1alpha1.NodeNetworkConfigSpec{
			Layer2s: map[string]v1alpha1.Layer2{
				"l2.100": {
					VNI:           100,
					AttachmentRef: &v1alpha1.Layer2AttachmentRef{Name: "green", Namespace: "tenant-a"},
				},
				"l2.200": {
					VNI:           200,
					AttachmentRef: &v1alpha1.Layer2AttachmentRef{Name: "blue", Namespace: "tenant-b"},
				},
			},
		},
	}

	entries := []v1alpha1.WorkloadPortEntry{
		*l2Entry("c1", "cra-green", "green", "tenant-a"),
	}

	if !MergeIntoNodeNetworkConfig(cfg, entries, logr.Discard()) {
		t.Fatal("expected merge to report a change")
	}

	green := cfg.Spec.Layer2s["l2.100"]
	if len(green.AttachedPorts) != 1 || green.AttachedPorts[0].Interface != "cra-green" {
		t.Fatalf("expected cra-green enslaved to l2.100, got %+v", green.AttachedPorts)
	}
	if blue := cfg.Spec.Layer2s["l2.200"]; len(blue.AttachedPorts) != 0 {
		t.Fatalf("expected no ports on non-matching l2.200, got %+v", blue.AttachedPorts)
	}
}

func TestMergeL2AttachDropsUnmatchedRef(t *testing.T) {
	cfg := &v1alpha1.NodeNetworkConfig{
		Spec: v1alpha1.NodeNetworkConfigSpec{
			Layer2s: map[string]v1alpha1.Layer2{
				"l2.100": {AttachmentRef: &v1alpha1.Layer2AttachmentRef{Name: "green", Namespace: "tenant-a"}},
			},
		},
	}

	// Ref that no Layer2 on the node carries: the port is dropped (the bridge
	// is a precondition owned by the L2A pipeline).
	entries := []v1alpha1.WorkloadPortEntry{
		*l2Entry("c1", "cra-absent", "missing", "tenant-z"),
	}

	MergeIntoNodeNetworkConfig(cfg, entries, logr.Discard())

	if l2 := cfg.Spec.Layer2s["l2.100"]; len(l2.AttachedPorts) != 0 {
		t.Fatalf("expected no ports enslaved for an unmatched ref, got %+v", l2.AttachedPorts)
	}
}

func TestMergeReportsWhetherPortsWereApplied(t *testing.T) {
	newCfg := func() *v1alpha1.NodeNetworkConfig {
		return &v1alpha1.NodeNetworkConfig{
			Spec: v1alpha1.NodeNetworkConfigSpec{
				Layer2s: map[string]v1alpha1.Layer2{
					"l2.100": {AttachmentRef: &v1alpha1.Layer2AttachmentRef{Name: "green", Namespace: "tenant-a"}},
				},
			},
		}
	}

	// A dropped L2 entry leaves the config without workload ports, so the merge
	// must not claim otherwise just because entries were supplied.
	dropped := []v1alpha1.WorkloadPortEntry{*l2Entry("c1", "cra-absent", "missing", "tenant-z")}
	if MergeIntoNodeNetworkConfig(newCfg(), dropped, logr.Discard()) {
		t.Fatal("expected false when every L2 entry was dropped")
	}

	applied := []v1alpha1.WorkloadPortEntry{*l2Entry("c1", "cra-green", "green", "tenant-a")}
	if !MergeIntoNodeNetworkConfig(newCfg(), applied, logr.Discard()) {
		t.Fatal("expected true when an L2 entry was enslaved")
	}

	if !MergeIntoNodeNetworkConfig(newCfg(), []v1alpha1.WorkloadPortEntry{*entry("c1", "eth1", "tenant-a")}, logr.Discard()) {
		t.Fatal("expected true for a routed entry")
	}

	if MergeIntoNodeNetworkConfig(newCfg(), nil, logr.Discard()) {
		t.Fatal("expected false for no entries")
	}
}

func TestHashEntriesStableAndSensitive(t *testing.T) {
	a := []v1alpha1.WorkloadPortEntry{*entry("c1", "eth1", "")}
	b := []v1alpha1.WorkloadPortEntry{*entry("c1", "eth1", "")}
	if HashEntries(a) != HashEntries(b) {
		t.Fatal("expected identical entries to hash equal")
	}

	c := []v1alpha1.WorkloadPortEntry{*entry("c1", "eth1", "tenant-a")}
	if HashEntries(a) == HashEntries(c) {
		t.Fatal("expected differing entries to hash differently")
	}

	// The hash is a set hash: neither entry order, host-route order nor a
	// nil-vs-empty slice may change it (it gates re-rendering).
	if HashEntries(nil) != HashEntries([]v1alpha1.WorkloadPortEntry{}) {
		t.Fatal("expected nil and empty entry slices to hash equal")
	}

	e1, e2 := *entry("c1", "eth1", ""), *entry("c2", "eth2", "")
	if HashEntries([]v1alpha1.WorkloadPortEntry{e1, e2}) != HashEntries([]v1alpha1.WorkloadPortEntry{e2, e1}) {
		t.Fatal("expected entry order not to change the hash")
	}

	reordered := *entry("c1", "eth1", "")
	reordered.HostRoutes = []string{"fd00:201::10/128", "10.201.0.10/32"}
	if HashEntries(a) != HashEntries([]v1alpha1.WorkloadPortEntry{reordered}) {
		t.Fatal("expected host-route order not to change the hash")
	}

	emptyRoutes := *entry("c1", "eth1", "")
	emptyRoutes.HostRoutes = []string{}
	nilRoutes := *entry("c1", "eth1", "")
	nilRoutes.HostRoutes = nil
	if HashEntries([]v1alpha1.WorkloadPortEntry{emptyRoutes}) != HashEntries([]v1alpha1.WorkloadPortEntry{nilRoutes}) {
		t.Fatal("expected nil and empty host routes to hash equal")
	}
}

func TestIsDefaultVRF(t *testing.T) {
	for _, name := range []string{"", "default", "main", "DEFAULT", "Main"} {
		if !isDefaultVRF(name) {
			t.Fatalf("expected %q to be default VRF", name)
		}
	}
	for _, name := range []string{"cluster", "tenant-a", "vrf1"} {
		if isDefaultVRF(name) {
			t.Fatalf("expected %q not to be default VRF", name)
		}
	}
}

// trunkEntry builds an L2 trunk entry. A nil vlan in vlans means the member
// inherits its Layer2's own VLAN id.
func trunkEntry(containerID, iface string, refs []string, vlans []*uint16) *v1alpha1.WorkloadPortEntry {
	e := &v1alpha1.WorkloadPortEntry{
		PodNamespace: "ns",
		PodName:      "pod",
		ContainerID:  containerID,
		WorkloadPort: v1alpha1.WorkloadPort{Interface: iface},
	}
	for i, name := range refs {
		e.Layer2Trunk = append(e.Layer2Trunk, v1alpha1.Layer2TrunkMember{
			Layer2AttachmentRef: v1alpha1.Layer2AttachmentRef{Name: name, Namespace: "tenant-a"},
			VLAN:                vlans[i],
		})
	}
	return e
}

func trunkCfg() *v1alpha1.NodeNetworkConfig {
	return &v1alpha1.NodeNetworkConfig{
		Spec: v1alpha1.NodeNetworkConfigSpec{
			Layer2s: map[string]v1alpha1.Layer2{
				"l2.100": {
					VNI: 100, VLAN: 100,
					AttachmentRef: &v1alpha1.Layer2AttachmentRef{Name: "green", Namespace: "tenant-a"},
				},
				"l2.200": {
					VNI: 200, VLAN: 200,
					AttachmentRef: &v1alpha1.Layer2AttachmentRef{Name: "red", Namespace: "tenant-a"},
				},
			},
		},
	}
}

// TestValidateLayer2AttachTrunkIdentityIsNamespaced pins the member identity
// to namespace/name, matching the CRD's list-map keys: the same name in two
// namespaces is two members, the same namespace/name twice is a duplicate.
func TestValidateLayer2AttachTrunkIdentityIsNamespaced(t *testing.T) {
	v100, v200 := uint16(100), uint16(200)
	member := func(ns string, vlan *uint16) v1alpha1.Layer2TrunkMember {
		return v1alpha1.Layer2TrunkMember{
			Layer2AttachmentRef: v1alpha1.Layer2AttachmentRef{Name: "foo", Namespace: ns},
			VLAN:                vlan,
		}
	}
	e := trunkEntry("c1", "cra0123456", nil, nil)

	e.Layer2Trunk = []v1alpha1.Layer2TrunkMember{member("tenant-a", &v100), member("tenant-b", &v200)}
	if err := validateLayer2Attach(e); err != nil {
		t.Fatalf("same name in different namespaces should be distinct members: %v", err)
	}

	e.Layer2Trunk = []v1alpha1.Layer2TrunkMember{member("tenant-a", &v100), member("tenant-a", &v200)}
	if err := validateLayer2Attach(e); err == nil {
		t.Fatal("same namespace/name twice should be rejected as a duplicate member")
	}
}

func TestValidateLayer2AttachBoundsTrunkMembers(t *testing.T) {
	refs := make([]string, 0, maxLayer2TrunkMembers+1)
	vlans := make([]*uint16, 0, maxLayer2TrunkMembers+1)
	for i := range maxLayer2TrunkMembers + 1 {
		vlan := uint16(i + 1) //nolint:gosec // bounded by maxLayer2TrunkMembers
		refs = append(refs, fmt.Sprintf("dom-%d", i))
		vlans = append(vlans, &vlan)
	}

	full := trunkEntry("c1", "cra0123456", refs[:maxLayer2TrunkMembers], vlans[:maxLayer2TrunkMembers])
	if err := validateLayer2Attach(full); err != nil {
		t.Fatalf("trunk with %d members should be valid: %v", maxLayer2TrunkMembers, err)
	}

	over := trunkEntry("c1", "cra0123456", refs, vlans)
	if err := validateLayer2Attach(over); err == nil {
		t.Fatalf("trunk with %d members should be rejected", maxLayer2TrunkMembers+1)
	}
}

func TestMergeL2TrunkResolvesVLANs(t *testing.T) {
	cfg := trunkCfg()
	translated := uint16(3000)
	entries := []v1alpha1.WorkloadPortEntry{
		*trunkEntry("c1", "cra-trunk", []string{"green", "red"}, []*uint16{nil, &translated}),
	}

	if !MergeIntoNodeNetworkConfig(cfg, entries, logr.Discard()) {
		t.Fatal("expected merge to report a change")
	}

	// No explicit vlan: the domain's own id is carried on the workload side.
	green := cfg.Spec.Layer2s["l2.100"]
	if len(green.AttachedPorts) != 1 || green.AttachedPorts[0].VLAN != 100 {
		t.Fatalf("expected cra-trunk on l2.100 with vlan 100, got %+v", green.AttachedPorts)
	}
	// Explicit vlan: the fabric-side 200 is translated to the workload-side 3000.
	red := cfg.Spec.Layer2s["l2.200"]
	if len(red.AttachedPorts) != 1 || red.AttachedPorts[0].VLAN != 3000 {
		t.Fatalf("expected cra-trunk on l2.200 with vlan 3000, got %+v", red.AttachedPorts)
	}
	if red.AttachedPorts[0].Interface != "cra-trunk" {
		t.Fatalf("expected interface cra-trunk, got %q", red.AttachedPorts[0].Interface)
	}
}

func TestMergeL2TrunkIsAllOrNothing(t *testing.T) {
	// One resolvable member and one that no Layer2 on the node carries: the
	// whole entry is dropped rather than half-wired.
	cfg := trunkCfg()
	entries := []v1alpha1.WorkloadPortEntry{
		*trunkEntry("c1", "cra-trunk", []string{"green", "missing"}, []*uint16{nil, nil}),
	}

	if MergeIntoNodeNetworkConfig(cfg, entries, logr.Discard()) {
		t.Fatal("expected merge to drop the entry")
	}
	if l2 := cfg.Spec.Layer2s["l2.100"]; len(l2.AttachedPorts) != 0 {
		t.Fatalf("expected no ports from a partially resolvable trunk, got %+v", l2.AttachedPorts)
	}
}

func TestMergeL2TrunkRejectsInheritedVLANCollision(t *testing.T) {
	// Translating green onto red's own id collides only after inheritance, so
	// neither the CNI nor the gRPC server can catch it.
	cfg := trunkCfg()
	collides := uint16(200)
	entries := []v1alpha1.WorkloadPortEntry{
		*trunkEntry("c1", "cra-trunk", []string{"green", "red"}, []*uint16{&collides, nil}),
	}

	if MergeIntoNodeNetworkConfig(cfg, entries, logr.Discard()) {
		t.Fatal("expected merge to drop the colliding trunk")
	}
	for name, l2 := range cfg.Spec.Layer2s {
		if len(l2.AttachedPorts) != 0 {
			t.Fatalf("expected no ports on %s, got %+v", name, l2.AttachedPorts)
		}
	}
}

func TestHashEntriesIsStableForReorderedTrunks(t *testing.T) {
	vlan := uint16(300)
	a := []v1alpha1.WorkloadPortEntry{
		*trunkEntry("c1", "cra-trunk", []string{"green", "red"}, []*uint16{nil, &vlan}),
	}
	reordered := []v1alpha1.WorkloadPortEntry{
		*trunkEntry("c1", "cra-trunk", []string{"red", "green"}, []*uint16{&vlan, nil}),
	}
	if HashEntries(a) != HashEntries(reordered) {
		t.Fatal("expected the hash to ignore trunk member order")
	}

	other := uint16(301)
	changed := []v1alpha1.WorkloadPortEntry{
		*trunkEntry("c1", "cra-trunk", []string{"green", "red"}, []*uint16{nil, &other}),
	}
	if HashEntries(a) == HashEntries(changed) {
		t.Fatal("expected a changed member vlan to change the hash")
	}
}

// mtuTrunkCfg is trunkCfg with the two domains sized differently: green carries
// jumbo frames, red does not.
func mtuTrunkCfg() *v1alpha1.NodeNetworkConfig {
	cfg := trunkCfg()
	green := cfg.Spec.Layer2s["l2.100"]
	green.MTU = 9000
	cfg.Spec.Layer2s["l2.100"] = green
	red := cfg.Spec.Layer2s["l2.200"]
	red.MTU = 1500
	cfg.Spec.Layer2s["l2.200"] = red
	return cfg
}

// TestMergeL2TrunkAcceptsMTUWithinEveryMember covers the trunk MTU rule: every
// sub-interface inherits the port MTU, so a trunk is accepted when each of its
// domains can carry the requested size.
func TestMergeL2TrunkAcceptsMTUWithinEveryMember(t *testing.T) {
	cfg := mtuTrunkCfg()
	e := trunkEntry("c1", "cra-trunk", []string{"green", "red"}, []*uint16{nil, nil})
	e.MTU = 1500

	if !MergeIntoNodeNetworkConfig(cfg, []v1alpha1.WorkloadPortEntry{*e}, logr.Discard()) {
		t.Fatal("expected merge to report a change")
	}
	for _, name := range []string{"l2.100", "l2.200"} {
		ports := cfg.Spec.Layer2s[name].AttachedPorts
		if len(ports) != 1 {
			t.Fatalf("expected cra-trunk on %s, got %+v", name, ports)
		}
		// The requested MTU reaches the datapath so the CRA can size the
		// sub-interfaces it derives from the port with it.
		if ports[0].MTU != 1500 {
			t.Errorf("attached port on %s has mtu %d, want 1500", name, ports[0].MTU)
		}
	}
}

// TestMergeL2TrunkRejectsMTUAboveAnyMember covers the other half of the rule:
// one member that cannot carry the requested size drops the attachment whole —
// the jumbo-capable member does not make the trunk safe for the 1500-byte one,
// whose bridge would black-hole every larger frame.
func TestMergeL2TrunkRejectsMTUAboveAnyMember(t *testing.T) {
	cfg := mtuTrunkCfg()
	e := trunkEntry("c1", "cra-trunk", []string{"green", "red"}, []*uint16{nil, nil})
	e.MTU = 9000

	if MergeIntoNodeNetworkConfig(cfg, []v1alpha1.WorkloadPortEntry{*e}, logr.Discard()) {
		t.Fatal("expected the oversized trunk to be dropped")
	}
	for _, name := range []string{"l2.100", "l2.200"} {
		if ports := cfg.Spec.Layer2s[name].AttachedPorts; len(ports) != 0 {
			t.Errorf("expected no attached port on %s, got %+v", name, ports)
		}
	}
}

// TestMergeL2AccessRejectsMTUAboveDomain covers the access-port rule: with a
// single domain there is nothing to fall back on, so it has to carry the whole
// requested MTU.
func TestMergeL2AccessRejectsMTUAboveDomain(t *testing.T) {
	cfg := mtuTrunkCfg()
	e := trunkEntry("c1", "cra-acc", nil, nil)
	e.Layer2AttachmentRef = &v1alpha1.Layer2AttachmentRef{Name: "red", Namespace: "tenant-a"}
	e.MTU = 9000

	if MergeIntoNodeNetworkConfig(cfg, []v1alpha1.WorkloadPortEntry{*e}, logr.Discard()) {
		t.Fatal("expected the oversized access port to be dropped")
	}
	if ports := cfg.Spec.Layer2s["l2.200"].AttachedPorts; len(ports) != 0 {
		t.Errorf("expected no attached port on l2.200, got %+v", ports)
	}

	// The same domain accepts the port at its own size.
	e.MTU = 1500
	if !MergeIntoNodeNetworkConfig(cfg, []v1alpha1.WorkloadPortEntry{*e}, logr.Discard()) {
		t.Fatal("expected the fitting access port to be merged")
	}
	if ports := cfg.Spec.Layer2s["l2.200"].AttachedPorts; len(ports) != 1 || ports[0].MTU != 1500 {
		t.Errorf("expected cra-acc on l2.200 with mtu 1500, got %+v", ports)
	}
}

// TestMergeRoutedIgnoresMTU covers routed attachments never being constrained by
// a domain MTU: they do not touch a bridge at all.
func TestMergeRoutedIgnoresMTU(t *testing.T) {
	cfg := mtuTrunkCfg()
	cfg.Spec.FabricVRFs = map[string]v1alpha1.FabricVRF{"tenant-a": {}}
	e := entry("c1", "cra-routed", "tenant-a")
	e.MTU = 9216

	if !MergeIntoNodeNetworkConfig(cfg, []v1alpha1.WorkloadPortEntry{*e}, logr.Discard()) {
		t.Fatal("expected the routed entry to be merged")
	}
}
