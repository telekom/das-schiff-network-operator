//go:build linux

package agent_hbn_l2

import (
	"testing"

	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
)

func TestParseVlanDetectsDisabledSegmentation(t *testing.T) {
	device := netplan.Device{Raw: []byte(`{
		"id": 100,
		"mtu": 1500,
		"generic-receive-offload": false,
		"generic-segmentation-offload": false,
		"tcp-segmentation-offload": false
	}`)}

	vlan, err := parseVlan(device)
	if err != nil {
		t.Fatalf("parseVlan returned error: %v", err)
	}

	if !vlan.disableSegmentation() {
		t.Fatal("expected disabled segmentation to be detected")
	}
}

func TestParseVlanLeavesSegmentationEnabledWhenUnset(t *testing.T) {
	device := netplan.Device{Raw: []byte(`{
		"id": 100,
		"mtu": 1500
	}`)}

	vlan, err := parseVlan(device)
	if err != nil {
		t.Fatalf("parseVlan returned error: %v", err)
	}

	if vlan.disableSegmentation() {
		t.Fatal("expected unset segmentation offloads to leave segmentation enabled")
	}
}
