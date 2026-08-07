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
	"testing"

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

	if !MergeIntoNodeNetworkConfig(cfg, entries) {
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
	MergeIntoNodeNetworkConfig(cfg, entries)
	if len(cfg.Spec.GlobalWorkloadPorts) != 1 {
		t.Fatalf("expected merge to be idempotent, got %+v", cfg.Spec.GlobalWorkloadPorts)
	}
	if fv := cfg.Spec.FabricVRFs["cluster"]; len(fv.WorkloadPorts) != 1 {
		t.Fatalf("expected merge to be idempotent on fabric VRF, got %+v", fv.WorkloadPorts)
	}

	// A subsequent merge with no entries clears the previously merged ports.
	MergeIntoNodeNetworkConfig(cfg, nil)
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

func TestMergeEmptyIsNoOp(t *testing.T) {
	cfg := &v1alpha1.NodeNetworkConfig{}
	if MergeIntoNodeNetworkConfig(cfg, nil) {
		t.Fatal("expected no change merging nil entries")
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
