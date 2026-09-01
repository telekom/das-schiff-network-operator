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

func TestVhostPortNameDeterministicAndBounded(t *testing.T) {
	const kernelIfNameLen = 15

	for _, tc := range []struct {
		name    string
		isTrunk bool
		wantLen int
	}{
		{name: "routed or access", wantLen: kernelIfNameLen},
		{name: "trunk", isTrunk: true, wantLen: kernelIfNameLen - len(maxTrunkVLANNameSuffix)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := vhostPortName("cid", "net1", tc.isTrunk)
			if len(name) != tc.wantLen {
				t.Errorf("vhostPortName %q length %d, want %d", name, len(name), tc.wantLen)
			}
			if tc.isTrunk && len(name+maxTrunkVLANNameSuffix) != kernelIfNameLen {
				t.Errorf("trunk sub-interface %q length %d, want %d",
					name+maxTrunkVLANNameSuffix, len(name+maxTrunkVLANNameSuffix), kernelIfNameLen)
			}
			if name != vhostPortName("cid", "net1", tc.isTrunk) {
				t.Errorf("vhostPortName is not deterministic for %q", tc.name)
			}
			if name == vhostPortName("cid", "net2", tc.isTrunk) {
				t.Error("vhostPortName must differ per pod-side interface")
			}
			if name == vhostPortName("other", "net1", tc.isTrunk) {
				t.Error("vhostPortName must differ per container")
			}
		})
	}
}

// TestVhostMAC pins the properties consumers rely on: the address is unicast,
// locally administered, a pure function of the attachment identity and
// distinct per interface of one sandbox.
func TestVhostMAC(t *testing.T) {
	a := vhostMAC("cid-1", "net1")
	if len(a) != 6 {
		t.Fatalf("len = %d, want 6", len(a))
	}
	if a[0]&0x01 != 0 {
		t.Errorf("%s is a multicast address", a)
	}
	if a[0]&0x02 == 0 {
		t.Errorf("%s is not locally administered", a)
	}
	if b := vhostMAC("cid-1", "net1"); b.String() != a.String() {
		t.Errorf("not deterministic: %s vs %s", a, b)
	}
	if c := vhostMAC("cid-1", "net2"); c.String() == a.String() {
		t.Errorf("net1 and net2 of one sandbox share %s", a)
	}
	if d := vhostMAC("cid-2", "net1"); d.String() == a.String() {
		t.Errorf("two sandboxes share %s", a)
	}
}
