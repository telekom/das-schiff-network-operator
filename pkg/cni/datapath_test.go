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
	"strconv"
	"testing"
)

func TestPortNameDeterministicAndBounded(t *testing.T) {
	// The generated CRA-side name is a real veth device name, and so is every
	// <port>.<vlan> a trunk derives from it. Its VSR infra-<portName> reference
	// is an ifalias, not a Linux interface name.
	const maxIfNameLen = 15
	id := "abc123def456containeridwithlotsofcharacters"

	a := portName(id, "net1")
	if b := portName(id, "net1"); a != b {
		t.Errorf("portName not deterministic: %q != %q", a, b)
	}
	if want := maxIfNameLen - len(maxTrunkVLANNameSuffix); len(a) != want {
		t.Errorf("portName %q length %d, want %d", a, len(a), want)
	}
	if len(a+maxTrunkVLANNameSuffix) > maxIfNameLen {
		t.Errorf("trunk sub-interface %q length %d exceeds %d",
			a+maxTrunkVLANNameSuffix, len(a+maxTrunkVLANNameSuffix), maxIfNameLen)
	}
	if portName("other-id", "net1") == a {
		t.Error("portName collision between distinct container IDs")
	}
	// The runtime (Multus) reuses one container ID for every attachment
	// of a pod, so the pod-side interface name must be part of the key.
	if portName(id, "net2") == a {
		t.Error("portName collision between distinct interfaces of one container")
	}
	// Zero-padding keeps the length stable for small hash values as well.
	for i := range 2000 {
		if n := portName(id, "net"+strconv.Itoa(i)); len(n) != len(a) {
			t.Fatalf("portName %q length %d differs from %d", n, len(n), len(a))
		}
	}
}

// TestResolveCRANetnsPathRejectsTraversal ensures a named CRA netns cannot
// escape the netns run directory: the plugin moves a veth into whatever it
// resolves, so "../../proc/1/ns/net" must not be accepted as a name.
func TestResolveCRANetnsPathRejectsTraversal(t *testing.T) {
	for _, spec := range []string{"..", ".", "../../proc/1/ns/net", "cra/../../proc/1/ns/net", "a/b"} {
		if _, err := resolveCRANetnsPath(&NetConf{NodeConfig: NodeConfig{CRANetns: spec}}); err == nil {
			t.Errorf("resolveCRANetnsPath(%q) accepted a non-plain netns name", spec)
		}
	}
}
