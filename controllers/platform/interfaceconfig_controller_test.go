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

package platform

import (
	"encoding/json"
	"testing"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

func ptr[T any](v T) *T { return &v }

func TestBuildNetplanEthernets(t *testing.T) {
	ethernets := map[string]nc.EthernetConfig{
		"eno1": {Mtu: ptr(int32(9000))},
		"eno2": {VirtualFunctionCount: ptr(int32(8))},
		"eno3": {Mtu: ptr(int32(1500)), VirtualFunctionCount: ptr(int32(4))},
	}

	result, err := buildNetplanEthernets(ethernets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(result))
	}

	// Check eno1 — MTU only.
	var eno1 map[string]interface{}
	if err := json.Unmarshal(result["eno1"].Raw, &eno1); err != nil {
		t.Fatalf("unmarshal eno1: %v", err)
	}
	if eno1["mtu"] != float64(9000) {
		t.Errorf("eno1 mtu: expected 9000, got %v", eno1["mtu"])
	}

	// Check eno2 — VF count only.
	var eno2 map[string]interface{}
	if err := json.Unmarshal(result["eno2"].Raw, &eno2); err != nil {
		t.Fatalf("unmarshal eno2: %v", err)
	}
	if eno2["virtual-function-count"] != float64(8) {
		t.Errorf("eno2 vf-count: expected 8, got %v", eno2["virtual-function-count"])
	}
}

func TestBuildNetplanEthernets_Empty(t *testing.T) {
	result, err := buildNetplanEthernets(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestBuildNetplanBonds(t *testing.T) {
	bonds := map[string]nc.BondConfig{
		"bond0": {
			Interfaces: []string{"eno1", "eno2"},
			Mtu:        ptr(int32(9000)),
			Parameters: &nc.BondParameters{
				Mode:               "802.3ad",
				LacpRate:           ptr("fast"),
				TransmitHashPolicy: ptr("layer3+4"),
				MiiMonitorInterval: ptr(int32(100)),
			},
		},
	}

	result, err := buildNetplanBonds(bonds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 device, got %d", len(result))
	}

	var bond0 map[string]interface{}
	if err := json.Unmarshal(result["bond0"].Raw, &bond0); err != nil {
		t.Fatalf("unmarshal bond0: %v", err)
	}

	if bond0["mtu"] != float64(9000) {
		t.Errorf("bond0 mtu: expected 9000, got %v", bond0["mtu"])
	}

	ifaces, ok := bond0["interfaces"].([]interface{})
	if !ok || len(ifaces) != 2 {
		t.Fatalf("bond0 interfaces: expected 2, got %v", bond0["interfaces"])
	}
	if ifaces[0] != "eno1" || ifaces[1] != "eno2" {
		t.Errorf("bond0 interfaces: expected [eno1, eno2], got %v", ifaces)
	}

	params, ok := bond0["parameters"].(map[string]interface{})
	if !ok {
		t.Fatalf("bond0 parameters: expected map, got %T", bond0["parameters"])
	}
	if params["mode"] != "802.3ad" {
		t.Errorf("bond0 mode: expected 802.3ad, got %v", params["mode"])
	}
	if params["lacp-rate"] != "fast" {
		t.Errorf("bond0 lacp-rate: expected fast, got %v", params["lacp-rate"])
	}
	if params["transmit-hash-policy"] != "layer3+4" {
		t.Errorf("bond0 transmit-hash-policy: expected layer3+4, got %v", params["transmit-hash-policy"])
	}
	if params["mii-monitor-interval"] != float64(100) {
		t.Errorf("bond0 mii-monitor-interval: expected 100, got %v", params["mii-monitor-interval"])
	}
}

func TestBuildNetplanBonds_NoParameters(t *testing.T) {
	bonds := map[string]nc.BondConfig{
		"bond0": {
			Interfaces: []string{"eno1"},
		},
	}

	result, err := buildNetplanBonds(bonds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var bond0 map[string]interface{}
	if err := json.Unmarshal(result["bond0"].Raw, &bond0); err != nil {
		t.Fatalf("unmarshal bond0: %v", err)
	}

	if _, hasParams := bond0["parameters"]; hasParams {
		t.Error("bond0 should not have parameters when nil")
	}
}
