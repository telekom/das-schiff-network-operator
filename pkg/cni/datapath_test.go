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
	"testing"
)

func TestPortNameDeterministicAndBounded(t *testing.T) {
	// The VSR references a netns-moved interface as infra-<portName>, and that
	// full port name must also fit the 15-char interface-name limit, so the
	// derived portName is bounded to 15 - len("infra-") = 9 characters.
	const maxIfNameLen = 15 - len("infra-")
	id := "abc123def456containeridwithlotsofcharacters"

	a := portName(id, "net1")
	b := portName(id, "net1")
	if a != b {
		t.Errorf("portName not deterministic: %q != %q", a, b)
	}
	if len(a) > maxIfNameLen {
		t.Errorf("portName %q length %d exceeds %d", a, len(a), maxIfNameLen)
	}
	if portName("other-id", "net1") == a {
		t.Errorf("portName collision between distinct container IDs")
	}
	// The runtime (Multus) reuses one container ID for every attachment of a
	// pod, so the pod-side interface name must be part of the key.
	if portName(id, "net2") == a {
		t.Errorf("portName collision between distinct interfaces of one container")
	}
}
