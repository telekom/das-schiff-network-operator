package agent_cra_frr //nolint:revive

import (
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

const (
	testMirrorVRF    = "mirror"
	testLoopbackName = "lo.mir"
	testLoopbackAddr = "10.255.0.1/32"
	testGREName      = "gtap-abc12345"
	testCollectorIP  = "192.168.99.1"
)

func strPtr(s string) *string { return &s }
func u16Ptr(v uint16) *uint16 { return &v }

// TestConvertNodeConfigToNetlink_MirrorACLs verifies that a NodeNetworkConfig
// carrying a Layer2 MirrorACL (referencing a GRE interface by name) plus the
// mirror VRF's GRE tunnel and loopback is translated into the corresponding
// netlink GRETunnel, LoopbackConfig and MirrorRule.
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
				"100": {
					VLAN: 100,
					VNI:  1000,
					MirrorACLs: []v1alpha1.MirrorACL{
						{
							MirrorDestination: testGREName,
							Direction:         v1alpha1.MirrorDirectionIngress,
						},
					},
				},
			},
			FabricVRFs: map[string]v1alpha1.FabricVRF{
				testMirrorVRF: {
					VRF: v1alpha1.VRF{
						Loopbacks: map[string]v1alpha1.Loopback{
							testLoopbackName: {IPAddresses: []string{testLoopbackAddr}},
						},
						GREs: map[string]v1alpha1.GRE{
							testGREName: {
								SourceAddress:      "10.255.0.1",
								SourceInterface:    testLoopbackName,
								DestinationAddress: testCollectorIP,
								Layer:              v1alpha1.GRELayer2,
							},
						},
					},
				},
			},
		},
	}

	netlinkConfig := applier.convertNodeConfigToNetlink(cfg)

	if len(netlinkConfig.Loopbacks) != 1 {
		t.Fatalf("expected 1 loopback, got %d", len(netlinkConfig.Loopbacks))
	}
	lo := netlinkConfig.Loopbacks[0]
	if lo.Name != testLoopbackName || lo.VRF != testMirrorVRF || lo.Addresses[0] != testLoopbackAddr {
		t.Errorf("unexpected loopback: %+v", lo)
	}

	if len(netlinkConfig.GRETunnels) != 1 {
		t.Fatalf("expected 1 GRE tunnel, got %d", len(netlinkConfig.GRETunnels))
	}
	tun := netlinkConfig.GRETunnels[0]
	if tun.Name != testGREName || tun.VRF != testMirrorVRF {
		t.Errorf("unexpected GRE tunnel name/VRF: %+v", tun)
	}
	if tun.Local != "10.255.0.1" || tun.Remote != testCollectorIP {
		t.Errorf("unexpected GRE endpoints: local=%q remote=%q", tun.Local, tun.Remote)
	}
	if tun.SourceInterface != testLoopbackName || !tun.Layer2 {
		t.Errorf("expected GRETAP bound to %q, got %+v", testLoopbackName, tun)
	}

	if len(netlinkConfig.Mirrors) != 1 {
		t.Fatalf("expected 1 mirror rule, got %d", len(netlinkConfig.Mirrors))
	}
	rule := netlinkConfig.Mirrors[0]
	if rule.SourceInterface != nl.MirrorSourceL2(100) {
		t.Errorf("mirror source = %q, want %q", rule.SourceInterface, nl.MirrorSourceL2(100))
	}
	if rule.GREInterface != testGREName {
		t.Errorf("mirror GRE interface = %q, want %q", rule.GREInterface, testGREName)
	}
	if rule.Direction != "ingress" {
		t.Errorf("mirror direction = %q, want ingress", rule.Direction)
	}
}

// TestConvertNodeConfigToNetlink_NoMirrors verifies that a config without mirror
// configuration produces no GRE tunnels, loopbacks or mirror rules.
func TestConvertNodeConfigToNetlink_NoMirrors(t *testing.T) {
	applier := &CRAFRRConfigApplier{
		baseConfig: &config.BaseConfig{ManagementVRF: config.BaseVRF{Name: "mgmt"}},
	}

	cfg := &v1alpha1.NodeNetworkConfig{
		Spec: v1alpha1.NodeNetworkConfigSpec{
			Layer2s: map[string]v1alpha1.Layer2{
				"100": {VLAN: 100, VNI: 1000},
			},
		},
	}

	netlinkConfig := applier.convertNodeConfigToNetlink(cfg)

	if len(netlinkConfig.Mirrors) != 0 {
		t.Errorf("expected 0 mirror rules, got %d", len(netlinkConfig.Mirrors))
	}
	if len(netlinkConfig.GRETunnels) != 0 {
		t.Errorf("expected 0 GRE tunnels, got %d", len(netlinkConfig.GRETunnels))
	}
	if len(netlinkConfig.Loopbacks) != 0 {
		t.Errorf("expected 0 loopbacks, got %d", len(netlinkConfig.Loopbacks))
	}
}

// TestConvertMirrorACL_AllFields verifies the MirrorACL → MirrorRule mapping for a
// fully-specified traffic match.
func TestConvertMirrorACL_AllFields(t *testing.T) {
	acl := v1alpha1.MirrorACL{
		MirrorDestination: testGREName,
		Direction:         v1alpha1.MirrorDirectionEgress,
		TrafficMatch: v1alpha1.TrafficMatch{
			Protocol:  strPtr("tcp"),
			SrcPrefix: strPtr("10.0.0.0/8"),
			DstPrefix: strPtr("192.168.0.0/16"),
			SrcPort:   u16Ptr(1234),
			DstPort:   u16Ptr(5678),
		},
	}

	rule := convertMirrorACL(&acl, "br.200")

	if rule.SourceInterface != "br.200" {
		t.Errorf("SourceInterface = %q, want br.200", rule.SourceInterface)
	}
	if rule.GREInterface != testGREName {
		t.Errorf("GREInterface = %q, want %s", rule.GREInterface, testGREName)
	}
	if rule.Direction != "egress" {
		t.Errorf("Direction = %q, want egress", rule.Direction)
	}
	if rule.Protocol != "tcp" || rule.SrcPrefix != "10.0.0.0/8" || rule.DstPrefix != "192.168.0.0/16" {
		t.Errorf("unexpected match fields: %+v", rule)
	}
	if rule.SrcPort != 1234 || rule.DstPort != 5678 {
		t.Errorf("unexpected ports: src=%d dst=%d", rule.SrcPort, rule.DstPort)
	}
}

// TestConvertMirrorACL_MinimalMatch verifies that an empty traffic match yields a
// rule with only the source, direction and GRE interface populated.
func TestConvertMirrorACL_MinimalMatch(t *testing.T) {
	acl := v1alpha1.MirrorACL{
		MirrorDestination: testGREName,
		Direction:         v1alpha1.MirrorDirectionIngress,
	}

	rule := convertMirrorACL(&acl, "prod")

	if rule.SourceInterface != "prod" || rule.GREInterface != testGREName || rule.Direction != "ingress" {
		t.Errorf("unexpected rule: %+v", rule)
	}
	if rule.Protocol != "" || rule.SrcPrefix != "" || rule.DstPrefix != "" || rule.SrcPort != 0 || rule.DstPort != 0 {
		t.Errorf("expected empty match, got %+v", rule)
	}
}
