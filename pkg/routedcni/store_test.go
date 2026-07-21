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

package routedcni

import (
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

func entry(containerID, iface, vrf string) *v1alpha1.RoutedPortEntry {
	return &v1alpha1.RoutedPortEntry{
		PodNamespace: "ns",
		PodName:      "pod",
		ContainerID:  containerID,
		VRF:          vrf,
		RoutedPort: v1alpha1.RoutedPort{
			Interface:  iface,
			GatewayV4:  "169.254.1.1/32",
			GatewayV6:  "fe80::1/128",
			HostRoutes: []string{"10.201.0.10/32", "fd00:201::10/128"},
		},
	}
}

func TestUpsertEntry(t *testing.T) {
	spec := &v1alpha1.NodeRoutedPortsSpec{}

	UpsertEntry(spec, entry("c1", "eth1", ""))
	if len(spec.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(spec.Ports))
	}

	// Same (containerID, interface) replaces in place.
	updated := entry("c1", "eth1", "")
	updated.HostRoutes = []string{"10.201.0.11/32"}
	UpsertEntry(spec, updated)
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
	spec := &v1alpha1.NodeRoutedPortsSpec{}
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

	entries := []v1alpha1.RoutedPortEntry{
		*entry("c1", "eth1", ""),         // -> underlay / ClusterVRF
		*entry("c2", "eth2", "cluster"),  // -> fabric VRF "cluster"
		*entry("c3", "eth3", "tenant-a"), // -> new local VRF
	}

	if !MergeIntoNodeNetworkConfig(cfg, entries) {
		t.Fatal("expected merge to report a change")
	}

	if cfg.Spec.ClusterVRF == nil || len(cfg.Spec.ClusterVRF.RoutedPorts) != 1 {
		t.Fatalf("expected 1 routed port on ClusterVRF, got %+v", cfg.Spec.ClusterVRF)
	}
	if fv := cfg.Spec.FabricVRFs["cluster"]; len(fv.RoutedPorts) != 1 {
		t.Fatalf("expected 1 routed port on fabric VRF cluster, got %+v", fv.RoutedPorts)
	}
	if lv, ok := cfg.Spec.LocalVRFs["tenant-a"]; !ok || len(lv.RoutedPorts) != 1 {
		t.Fatalf("expected 1 routed port on local VRF tenant-a, got %+v", cfg.Spec.LocalVRFs)
	}
}

func TestMergeEmptyIsNoOp(t *testing.T) {
	cfg := &v1alpha1.NodeNetworkConfig{}
	if MergeIntoNodeNetworkConfig(cfg, nil) {
		t.Fatal("expected no change merging nil entries")
	}
}

func l2Entry(containerID, iface, l2aName, l2aNamespace string) *v1alpha1.RoutedPortEntry {
	return &v1alpha1.RoutedPortEntry{
		PodNamespace: "ns",
		PodName:      "pod",
		ContainerID:  containerID,
		Layer2AttachmentRef: &v1alpha1.Layer2AttachmentRef{
			Name:      l2aName,
			Namespace: l2aNamespace,
		},
		RoutedPort: v1alpha1.RoutedPort{Interface: iface},
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

	entries := []v1alpha1.RoutedPortEntry{
		*l2Entry("c1", "cra-green", "green", "tenant-a"),
	}

	if !MergeIntoNodeNetworkConfig(cfg, entries) {
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
	entries := []v1alpha1.RoutedPortEntry{
		*l2Entry("c1", "cra-absent", "missing", "tenant-z"),
	}

	MergeIntoNodeNetworkConfig(cfg, entries)

	if l2 := cfg.Spec.Layer2s["l2.100"]; len(l2.AttachedPorts) != 0 {
		t.Fatalf("expected no ports enslaved for an unmatched ref, got %+v", l2.AttachedPorts)
	}
}

func TestHashEntriesStableAndSensitive(t *testing.T) {
	a := []v1alpha1.RoutedPortEntry{*entry("c1", "eth1", "")}
	b := []v1alpha1.RoutedPortEntry{*entry("c1", "eth1", "")}
	if HashEntries(a) != HashEntries(b) {
		t.Fatal("expected identical entries to hash equal")
	}

	c := []v1alpha1.RoutedPortEntry{*entry("c1", "eth1", "tenant-a")}
	if HashEntries(a) == HashEntries(c) {
		t.Fatal("expected differing entries to hash differently")
	}

	if HashEntries(nil) != HashEntries([]v1alpha1.RoutedPortEntry{}) {
		t.Log("nil vs empty may differ; only equality of identical slices is required")
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
