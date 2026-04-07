package agent_cra_frr //nolint:revive

import (
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

const (
	testMirrorVRF    = "mirror"
	testLoopbackAddr = "10.255.0.1/32"
)

func strPtr(s string) *string { return &s }
func u16Ptr(v uint16) *uint16 { return &v }

func TestConvertNodeConfigToNetlink_MirrorACLs(t *testing.T) {
	applier := &CRAFRRConfigApplier{
		baseConfig: &config.BaseConfig{
			ManagementVRF: config.BaseVRF{Name: "mgmt"},
		},
	}

	cfg := &v1alpha1.NodeNetworkConfig{
		Spec: v1alpha1.NodeNetworkConfigSpec{
			Revision: "test",
			Layer2s: map[string]v1alpha1.Layer2{
				"l2a-web": {
					VNI:  10100,
					VLAN: 100,
					MTU:  9000,
					MirrorACLs: []v1alpha1.MirrorACL{
						{
							TrafficMatch: v1alpha1.TrafficMatch{
								Protocol:  strPtr("tcp"),
								DstPort:   u16Ptr(80),
								SrcPrefix: strPtr("10.0.0.0/24"),
							},
							DestinationAddress: "192.168.99.1",
							DestinationVrf:     testMirrorVRF,
							EncapsulationType:  v1alpha1.EncapsulationTypeGRE,
							Direction:          "ingress",
						},
					},
				},
			},
			FabricVRFs: map[string]v1alpha1.FabricVRF{
				testMirrorVRF: {
					VRF: v1alpha1.VRF{
						Loopbacks: map[string]v1alpha1.Loopback{
							testMirrorVRF: {IPAddresses: []string{testLoopbackAddr}},
						},
						MirrorACLs: []v1alpha1.MirrorACL{
							{
								TrafficMatch: v1alpha1.TrafficMatch{
									Protocol: strPtr("icmp"),
								},
								DestinationAddress: "192.168.99.1",
								DestinationVrf:     testMirrorVRF,
								EncapsulationType:  v1alpha1.EncapsulationTypeGRE,
								Direction:          "egress",
							},
						},
					},
					VNI:                    10099,
					EVPNImportRouteTargets: []string{"65000:10099"},
					EVPNExportRouteTargets: []string{"65000:10099"},
				},
				"prod": {
					VRF:                    v1alpha1.VRF{},
					VNI:                    10001,
					EVPNImportRouteTargets: []string{"65000:10001"},
					EVPNExportRouteTargets: []string{"65000:10001"},
				},
			},
		},
	}

	result := applier.convertNodeConfigToNetlink(cfg)

	// Verify L2 mirrors
	if len(result.Mirrors) != 2 {
		t.Fatalf("expected 2 mirror rules, got %d", len(result.Mirrors))
	}

	// Find the L2 mirror rule (source = br.100)
	var l2Rule, vrfRule *nl.MirrorRule
	for i := range result.Mirrors {
		if result.Mirrors[i].SourceInterface == "br.100" {
			l2Rule = &result.Mirrors[i]
		}
		if result.Mirrors[i].SourceInterface == testMirrorVRF {
			vrfRule = &result.Mirrors[i]
		}
	}

	if l2Rule == nil {
		t.Fatal("L2 mirror rule (br.100) not found")
	}
	if l2Rule.GRERemote != "192.168.99.1" {
		t.Errorf("L2 GRERemote = %q, want 192.168.99.1", l2Rule.GRERemote)
	}
	if l2Rule.GRELocal != testLoopbackAddr {
		t.Errorf("L2 GRELocal = %q, want %s", l2Rule.GRELocal, testLoopbackAddr)
	}
	if l2Rule.GREVRF != testMirrorVRF {
		t.Errorf("L2 GREVRF = %q, want %s", l2Rule.GREVRF, testMirrorVRF)
	}
	if l2Rule.Protocol != "tcp" {
		t.Errorf("L2 Protocol = %q, want tcp", l2Rule.Protocol)
	}
	if l2Rule.DstPort != 80 {
		t.Errorf("L2 DstPort = %d, want 80", l2Rule.DstPort)
	}
	if l2Rule.SrcPrefix != "10.0.0.0/24" {
		t.Errorf("L2 SrcPrefix = %q, want 10.0.0.0/24", l2Rule.SrcPrefix)
	}
	if l2Rule.Direction != "ingress" {
		t.Errorf("L2 Direction = %q, want ingress", l2Rule.Direction)
	}

	// Verify VRF mirror rule
	if vrfRule == nil {
		t.Fatal("VRF mirror rule (mirror) not found")
	}
	if vrfRule.Protocol != "icmp" {
		t.Errorf("VRF Protocol = %q, want icmp", vrfRule.Protocol)
	}
	if vrfRule.Direction != "egress" {
		t.Errorf("VRF Direction = %q, want egress", vrfRule.Direction)
	}

	// Verify loopbacks
	if len(result.Loopbacks) != 1 {
		t.Fatalf("expected 1 loopback config, got %d", len(result.Loopbacks))
	}
	lo := result.Loopbacks[0]
	wantLoName := "lo." + testMirrorVRF
	if lo.Name != wantLoName {
		t.Errorf("loopback Name = %q, want %s", lo.Name, wantLoName)
	}
	if lo.VRF != testMirrorVRF {
		t.Errorf("loopback VRF = %q, want %s", lo.VRF, testMirrorVRF)
	}
	if len(lo.Addresses) != 1 || lo.Addresses[0] != testLoopbackAddr {
		t.Errorf("loopback Addresses = %v, want [%s]", lo.Addresses, testLoopbackAddr)
	}

	// Verify VRFs (should include mirror and prod, but not mgmt)
	vrfNames := make(map[string]bool)
	for _, v := range result.VRFs {
		vrfNames[v.Name] = true
	}
	if vrfNames["mgmt"] {
		t.Error("management VRF should be excluded")
	}
	if !vrfNames[testMirrorVRF] {
		t.Error("mirror VRF missing")
	}
	if !vrfNames["prod"] {
		t.Error("prod VRF missing")
	}
}

func TestConvertNodeConfigToNetlink_NoMirrors(t *testing.T) {
	applier := &CRAFRRConfigApplier{
		baseConfig: &config.BaseConfig{
			ManagementVRF: config.BaseVRF{Name: "mgmt"},
		},
	}

	cfg := &v1alpha1.NodeNetworkConfig{
		Spec: v1alpha1.NodeNetworkConfigSpec{
			Revision: "test",
			Layer2s: map[string]v1alpha1.Layer2{
				"l2a-web": {VNI: 10100, VLAN: 100, MTU: 9000},
			},
			FabricVRFs: map[string]v1alpha1.FabricVRF{
				"prod": {VRF: v1alpha1.VRF{}, VNI: 10001,
					EVPNImportRouteTargets: []string{"65000:10001"},
					EVPNExportRouteTargets: []string{"65000:10001"}},
			},
		},
	}

	result := applier.convertNodeConfigToNetlink(cfg)

	if len(result.Mirrors) != 0 {
		t.Errorf("expected 0 mirrors, got %d", len(result.Mirrors))
	}
	if len(result.Loopbacks) != 0 {
		t.Errorf("expected 0 loopbacks, got %d", len(result.Loopbacks))
	}
}

func TestConvertMirrorACL_AllFields(t *testing.T) {
	acl := v1alpha1.MirrorACL{
		TrafficMatch: v1alpha1.TrafficMatch{
			Protocol:  strPtr("udp"),
			SrcPrefix: strPtr("10.0.0.0/8"),
			DstPrefix: strPtr("172.16.0.0/12"),
			SrcPort:   u16Ptr(1234),
			DstPort:   u16Ptr(5678),
		},
		DestinationAddress: "192.168.99.5",
		DestinationVrf:     "mir-vrf",
		EncapsulationType:  v1alpha1.EncapsulationTypeGRE,
		Direction:          "egress",
	}

	vrfLoopbacks := map[string]string{"mir-vrf": testLoopbackAddr}
	rule := convertMirrorACL(&acl, "br.200", vrfLoopbacks)

	if rule.SourceInterface != "br.200" {
		t.Errorf("SourceInterface = %q, want br.200", rule.SourceInterface)
	}
	if rule.GRERemote != "192.168.99.5" {
		t.Errorf("GRERemote = %q, want 192.168.99.5", rule.GRERemote)
	}
	if rule.GRELocal != testLoopbackAddr {
		t.Errorf("GRELocal = %q, want %s", rule.GRELocal, testLoopbackAddr)
	}
	if rule.GREVRF != "mir-vrf" {
		t.Errorf("GREVRF = %q, want mir-vrf", rule.GREVRF)
	}
	if rule.Protocol != "udp" {
		t.Errorf("Protocol = %q, want udp", rule.Protocol)
	}
	if rule.SrcPrefix != "10.0.0.0/8" {
		t.Errorf("SrcPrefix = %q, want 10.0.0.0/8", rule.SrcPrefix)
	}
	if rule.DstPrefix != "172.16.0.0/12" {
		t.Errorf("DstPrefix = %q, want 172.16.0.0/12", rule.DstPrefix)
	}
	if rule.SrcPort != 1234 {
		t.Errorf("SrcPort = %d, want 1234", rule.SrcPort)
	}
	if rule.DstPort != 5678 {
		t.Errorf("DstPort = %d, want 5678", rule.DstPort)
	}
	if rule.Direction != "egress" {
		t.Errorf("Direction = %q, want egress", rule.Direction)
	}
}

func TestConvertMirrorACL_MinimalMatch(t *testing.T) {
	acl := v1alpha1.MirrorACL{
		DestinationAddress: "192.168.99.1",
		DestinationVrf:     testMirrorVRF,
		EncapsulationType:  v1alpha1.EncapsulationTypeGRE,
	}

	rule := convertMirrorACL(&acl, "prod", map[string]string{testMirrorVRF: "10.0.0.1/32"})

	if rule.Protocol != "" {
		t.Errorf("Protocol should be empty, got %q", rule.Protocol)
	}
	if rule.SrcPrefix != "" {
		t.Errorf("SrcPrefix should be empty, got %q", rule.SrcPrefix)
	}
	if rule.DstPrefix != "" {
		t.Errorf("DstPrefix should be empty, got %q", rule.DstPrefix)
	}
	if rule.SrcPort != 0 {
		t.Errorf("SrcPort should be 0, got %d", rule.SrcPort)
	}
	if rule.DstPort != 0 {
		t.Errorf("DstPort should be 0, got %d", rule.DstPort)
	}
	if rule.Direction != "both" {
		t.Errorf("Direction should be both (default), got %q", rule.Direction)
	}
}
