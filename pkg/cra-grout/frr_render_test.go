package cra

import (
	"strings"
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	crafrr "github.com/telekom/das-schiff-network-operator/pkg/cra-frr"
)

// TestFRRTemplateEmitsExplicitRouteTargets guards a Phase-7 finding: eBGP EVPN
// auto-RTs (AS:VNI) differ across ASNs, so type-5 (L3VNI) routes are received
// but NOT imported into the tenant VRF unless the fabric-facing FRR config emits
// EXPLICIT `route-target import/export` per VNI. The cra-grout flavor renders its
// control plane through the shared cra-frr template; this test asserts the grout
// template still emits per-VNI RTs (fabric L3VNI + L2VNI) and advertise-all-vni,
// so a future template edit can't silently reintroduce the auto-RT mismatch.
func TestFRRTemplateEmitsExplicitRouteTargets(t *testing.T) {
	tpl := crafrr.FRRTemplate{FRRTemplatePath: "../../config/agent-cra-grout/frr.conf.tpl"}

	base := &config.BaseConfig{
		VTEPLoopbackIP: "10.50.0.10",
		LocalASN:       65000,
		ClusterVRF:     config.BaseVRF{Name: "cluster", VNI: 100, EVPNRouteTarget: "65000:100"},
		ManagementVRF:  config.BaseVRF{Name: "mgmt", VNI: 999, EVPNRouteTarget: "65000:999"},
	}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		FabricVRFs: map[string]v1alpha1.FabricVRF{
			"tenant-a": {
				VRF:                    v1alpha1.VRF{},
				VNI:                    5000,
				EVPNImportRouteTargets: []string{"65000:5000"},
				EVPNExportRouteTargets: []string{"65000:5000"},
			},
		},
		Layer2s: map[string]v1alpha1.Layer2{
			"blue": {
				VNI:         2000,
				VLAN:        100,
				RouteTarget: "65000:2000",
				IRB:         &v1alpha1.IRB{VRF: "tenant-a", IPAddresses: []string{"10.0.0.1/24"}},
			},
		},
	}

	out, err := tpl.TemplateFRR(base, spec)
	if err != nil {
		t.Fatalf("TemplateFRR: %v", err)
	}

	mustContain := []string{
		"advertise-all-vni",
		// Fabric L3VNI (tenant-a) explicit RTs -> deterministic type-5 import.
		"route-target import 65000:5000",
		"route-target export 65000:5000",
		// L2VNI explicit RTs -> deterministic type-2/3 import.
		"route-target export 65000:2000",
		"route-target import 65000:2000",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("rendered FRR config missing %q\n---\n%s", want, out)
		}
	}
}
