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
	const maxIfNameLen = 15
	id := "abc123def456containeridwithlotsofcharacters"

	a := portName(id)
	b := portName(id)
	if a != b {
		t.Errorf("portName not deterministic: %q != %q", a, b)
	}
	if len(a) > maxIfNameLen {
		t.Errorf("portName %q length %d exceeds %d", a, len(a), maxIfNameLen)
	}
	if portName("other-id") == a {
		t.Errorf("portName collision between distinct container IDs")
	}
}
