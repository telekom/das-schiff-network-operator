package agent_cra_frr //nolint:revive

import (
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

func TestConvertLayer2AnycastMAC(t *testing.T) {
	applier := &CRAFRRConfigApplier{baseConfig: &config.BaseConfig{}}
	cfg := &v1alpha1.NodeNetworkConfig{Spec: v1alpha1.NodeNetworkConfigSpec{Layer2s: map[string]v1alpha1.Layer2{
		"3001": {VLAN: 3001, VNI: 3001, MTU: 1500},
		"3002": {VLAN: 3002, VNI: 3002, MTU: 1500, IRB: &v1alpha1.IRB{VRF: "tenant", MACAddress: "02:00:00:00:30:02"}},
	}}}
	for _, l2 := range applier.convertNodeConfigToNetlink(cfg).Layer2s {
		switch l2.VlanID {
		case 3001:
			if l2.AnycastMAC != nil {
				t.Errorf("Layer2 without IRB: AnycastMAC = %q, want nil", *l2.AnycastMAC)
			}
		case 3002:
			if l2.AnycastMAC == nil || *l2.AnycastMAC != "02:00:00:00:30:02" || l2.VRF != "tenant" {
				t.Errorf("Layer2 with IRB: AnycastMAC %v, VRF %q", l2.AnycastMAC, l2.VRF)
			}
		}
	}
}
