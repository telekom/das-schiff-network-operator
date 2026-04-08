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

package cra

import (
	"testing"
)

// Regression tests for the IPv6 Sort bug where Physical.Sort(), Bridge.Sort(),
// and VXLAN.Sort() incorrectly called IPv4.Sort() instead of IPv6.Sort() when
// IPv6 != nil. Fixed in commit d56b937.

func makeUnsortedIPv4() *IPAddressList {
	return &IPAddressList{
		IPAddresses: []IPAddress{
			{IP: "192.168.1.30"},
			{IP: "10.0.0.5"},
			{IP: "172.16.0.1"},
		},
	}
}

func makeUnsortedIPv6() *IPAddressList {
	return &IPAddressList{
		IPAddresses: []IPAddress{
			{IP: "fd00::30"},
			{IP: "2001:db8::1"},
			{IP: "::1"},
		},
	}
}

func checkSorted(t *testing.T, label string, list *IPAddressList) {
	t.Helper()
	for i := 1; i < len(list.IPAddresses); i++ {
		if list.IPAddresses[i-1].IP > list.IPAddresses[i].IP {
			t.Errorf("%s: IPAddresses not sorted at index %d: %q > %q",
				label, i, list.IPAddresses[i-1].IP, list.IPAddresses[i].IP)
		}
	}
}

// TestPhysicalSort_IPv6Regression verifies that Physical.Sort() correctly
// sorts both the IPv4 and IPv6 address lists independently.
func TestPhysicalSort_IPv6Regression(t *testing.T) {
	phys := &Physical{
		Name: "eth0",
		IPv4: makeUnsortedIPv4(),
		IPv6: makeUnsortedIPv6(),
	}

	phys.Sort()

	checkSorted(t, "Physical.IPv4", phys.IPv4)
	checkSorted(t, "Physical.IPv6", phys.IPv6)
}

// TestBridgeSort_IPv6Regression verifies that Bridge.Sort() correctly
// sorts both the IPv4 and IPv6 address lists independently.
func TestBridgeSort_IPv6Regression(t *testing.T) {
	br := &Bridge{
		Name: "br0",
		IPv4: makeUnsortedIPv4(),
		IPv6: makeUnsortedIPv6(),
	}

	br.Sort()

	checkSorted(t, "Bridge.IPv4", br.IPv4)
	checkSorted(t, "Bridge.IPv6", br.IPv6)
}

// TestVXLANSort_IPv6Regression verifies that VXLAN.Sort() correctly
// sorts both the IPv4 and IPv6 address lists independently.
func TestVXLANSort_IPv6Regression(t *testing.T) {
	vx := &VXLAN{
		Name: "vx0",
		VNI:  100,
		IPv4: makeUnsortedIPv4(),
		IPv6: makeUnsortedIPv6(),
	}

	vx.Sort()

	checkSorted(t, "VXLAN.IPv4", vx.IPv4)
	checkSorted(t, "VXLAN.IPv6", vx.IPv6)
}

// TestPhysicalSort_IPv6Only verifies that Physical.Sort() handles the case
// where only IPv6 is set (IPv4 is nil) without panicking.
func TestPhysicalSort_IPv6Only(t *testing.T) {
	phys := &Physical{
		Name: "eth0",
		IPv4: nil,
		IPv6: makeUnsortedIPv6(),
	}

	phys.Sort()

	checkSorted(t, "Physical.IPv6-only", phys.IPv6)
}

// TestBridgeSort_IPv6Only verifies that Bridge.Sort() handles the case
// where only IPv6 is set (IPv4 is nil) without panicking.
func TestBridgeSort_IPv6Only(t *testing.T) {
	br := &Bridge{
		Name: "br0",
		IPv4: nil,
		IPv6: makeUnsortedIPv6(),
	}

	br.Sort()

	checkSorted(t, "Bridge.IPv6-only", br.IPv6)
}

// TestVXLANSort_IPv6Only verifies that VXLAN.Sort() handles the case
// where only IPv6 is set (IPv4 is nil) without panicking.
func TestVXLANSort_IPv6Only(t *testing.T) {
	vx := &VXLAN{
		Name: "vx0",
		VNI:  100,
		IPv4: nil,
		IPv6: makeUnsortedIPv6(),
	}

	vx.Sort()

	checkSorted(t, "VXLAN.IPv6-only", vx.IPv6)
}
