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
	// The generated CRA-side name is a real veth device name. Its VSR
	// infra-<portName> reference is an ifalias, not a Linux interface name.
	const maxIfNameLen = 15
	id := "abc123def456containeridwithlotsofcharacters"

	for _, tc := range []struct {
		name    string
		isTrunk bool
		wantLen int
	}{
		{name: "routed or access", wantLen: maxIfNameLen},
		{name: "trunk", isTrunk: true, wantLen: maxIfNameLen - len(maxTrunkVLANNameSuffix)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := portName(id, "net1", tc.isTrunk)
			b := portName(id, "net1", tc.isTrunk)
			if a != b {
				t.Errorf("portName not deterministic: %q != %q", a, b)
			}
			if len(a) != tc.wantLen {
				t.Errorf("portName %q length %d, want %d", a, len(a), tc.wantLen)
			}
			if len(a) > maxIfNameLen {
				t.Errorf("portName %q length %d exceeds %d", a, len(a), maxIfNameLen)
			}
			if tc.isTrunk && len(a+maxTrunkVLANNameSuffix) != maxIfNameLen {
				t.Errorf("trunk sub-interface %q length %d, want %d",
					a+maxTrunkVLANNameSuffix, len(a+maxTrunkVLANNameSuffix), maxIfNameLen)
			}
			if portName("other-id", "net1", tc.isTrunk) == a {
				t.Error("portName collision between distinct container IDs")
			}
			// The runtime (Multus) reuses one container ID for every attachment
			// of a pod, so the pod-side interface name must be part of the key.
			if portName(id, "net2", tc.isTrunk) == a {
				t.Error("portName collision between distinct interfaces of one container")
			}
		})
	}
}
