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
	"context"
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

const (
	testWorkNS  = "hbn"
	testVRF     = "t_m2m"
	testVLAN1   = "vlan.1001"
	testVLAN2   = "vlan.1002"
	testBridge1 = "l2.1001"
	testBridge2 = "l2.1002"
	testVXLAN1  = "vx.3022001"
	testVXLAN2  = "vx.3022002"
)

// buildFullyAppliedNS builds a working namespace that mirrors what
// Layer2.setup() would build for two VLANs (1001, 1002) inside VRF "t_m2m",
// with everything correctly applied/enslaved - i.e. the healthy case.
func buildFullyAppliedNS() *Namespace {
	ns := &Namespace{
		Name: testWorkNS,
		Interfaces: &Interfaces{
			VXLANs: []VXLAN{
				{Name: testVXLAN1},
				{Name: testVXLAN2},
			},
			VLANs: []VLAN{
				{Name: testVLAN1, VlanID: 1001},
				{Name: testVLAN2, VlanID: 1002},
			},
		},
		VRFs: []VRF{
			{
				Name: testVRF,
				Interfaces: &Interfaces{
					Bridges: []Bridge{
						{
							Name: testBridge1,
							Slaves: []BridgeSlave{
								{Name: testVXLAN1},
								{Name: testVLAN1},
							},
						},
						{
							Name: testBridge2,
							Slaves: []BridgeSlave{
								{Name: testVXLAN2},
								{Name: testVLAN2},
							},
						},
					},
				},
			},
		},
	}
	return ns
}

func irbLayer2(vlan uint16, vni uint32, vrf string) v1alpha1.Layer2 {
	return v1alpha1.Layer2{
		VLAN: vlan,
		VNI:  vni,
		MTU:  1500,
		IRB: &v1alpha1.IRB{
			VRF:        vrf,
			MACAddress: "aa:bb:cc:dd:ee:ff",
			IPAddresses: []string{
				"10.0.0.1/24",
			},
		},
	}
}

func TestVerifyLayer2EntryFullyApplied(t *testing.T) {
	ns := buildFullyAppliedNS()

	for _, l2 := range []v1alpha1.Layer2{
		irbLayer2(1001, 3022001, testVRF),
		irbLayer2(1002, 3022002, testVRF),
	} {
		l2 := l2
		if err := verifyLayer2Entry(ns, &l2); err != nil {
			t.Errorf("expected vlan %d to verify as applied, got error: %v", l2.VLAN, err)
		}
	}
}

// TestVerifyLayer2EntryMissingBridge reproduces the exact incident symptom:
// vlan 1002's bridge (and its vxlan) were never instantiated by the VSR,
// while the sibling vlan 1001 landed correctly. Before this fix, a NETCONF
// commit returning "ok" would be treated as full success and the
// NodeNetworkConfig would be marked "provisioned" despite this gap.
func TestVerifyLayer2EntryMissingBridge(t *testing.T) {
	ns := buildFullyAppliedNS()

	// Remove vlan 1002's bridge and vxlan to simulate the partial/incomplete
	// VSR provisioning race, leaving vlan.1002 present but without a master.
	vrf := LookupVRF(ns, testVRF)
	vrf.Interfaces.Bridges = vrf.Interfaces.Bridges[:1] // keep only l2.1001
	ns.Interfaces.VXLANs = ns.Interfaces.VXLANs[:1]     // keep only vx.3022001

	l2 := irbLayer2(1002, 3022002, testVRF)
	err := verifyLayer2Entry(ns, &l2)
	if err == nil {
		t.Fatal("expected verification to fail for vlan 1002 with missing bridge, got nil error")
	}
}

func TestVerifyLayer2EntryVRFNotFound(t *testing.T) {
	ns := buildFullyAppliedNS()

	l2 := irbLayer2(1002, 3022002, "does_not_exist")
	err := verifyLayer2Entry(ns, &l2)
	if err == nil {
		t.Fatal("expected verification to fail for a VRF that does not exist in running config")
	}
}

func TestVerifyLayer2EntryBridgeNotEnslaved(t *testing.T) {
	ns := buildFullyAppliedNS()

	// Bridge and vxlan/vlan interfaces all exist, but the bridge's slave list
	// is missing the vxlan - i.e. the objects were created but never wired
	// together (another partial-apply variant).
	vrf := LookupVRF(ns, testVRF)
	br := LookupBridge(vrf.Interfaces, testBridge2)
	br.Slaves = []BridgeSlave{{Name: testVLAN2}} // drop vx.3022002 slave

	l2 := irbLayer2(1002, 3022002, testVRF)
	err := verifyLayer2Entry(ns, &l2)
	if err == nil {
		t.Fatal("expected verification to fail when vxlan is not enslaved to the bridge")
	}
}

func TestVerifyLayer2AppliedNoLayer2s(t *testing.T) {
	m := &Manager{WorkNSName: testWorkNS}
	// nodeCfg with no Layer2s should be a no-op and never touch NETCONF.
	nodeCfg := &v1alpha1.NodeNetworkConfigSpec{}
	if err := m.verifyLayer2Applied(context.TODO(), nodeCfg); err != nil {
		t.Fatalf("expected no error for empty Layer2s, got: %v", err)
	}
}
