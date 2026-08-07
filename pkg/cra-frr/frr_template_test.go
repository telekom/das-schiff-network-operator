package cra

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/utils/ptr"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

const (
	operatorFRRTemplate = "../../config/agent-cra-frr/frr.conf.tpl"
	localASN            = 65501
)

func testBaseConfig(peerIPv6 bool) *config.BaseConfig {
	return &config.BaseConfig{
		LocalASN:       localASN,
		VTEPLoopbackIP: "10.0.0.1",
		UnderlayNeighbors: []config.Neighbor{
			{Interface: ptr.To("eth1"), RemoteASN: "65500", LocalASN: ptr.To("65501"), IPv4: true, IPv6: peerIPv6},
		},
	}
}

func renderFRR(t *testing.T, peerIPv6 bool, nodeConfig *v1alpha1.NodeNetworkConfigSpec) string {
	t.Helper()

	rendered, err := FRRTemplate{FRRTemplatePath: operatorFRRTemplate}.TemplateFRR(testBaseConfig(peerIPv6), nodeConfig)
	if err != nil {
		t.Fatalf("failed to render FRR template: %v", err)
	}

	return rendered
}

// defaultVRFSection returns the part of the config belonging to the default VRF
// "router bgp", i.e. everything before the first VRF-scoped instance.
func defaultVRFSection(t *testing.T, rendered string) string {
	t.Helper()

	_, after, found := strings.Cut(rendered, fmt.Sprintf("router bgp %d\n", localASN))
	if !found {
		t.Fatal("rendered config has no default VRF router bgp instance")
	}

	if before, _, ok := strings.Cut(after, "\nrouter bgp "); ok {
		return before
	}

	return after
}

// Global (VRF-less) workload ports live in the CRA netns main table. They are
// only reachable from the fabric if the CRA originates their host routes into
// the underlay, so the default VRF must advertise them.
func TestTemplateFRRAdvertisesGlobalWorkloadPortHostRoutes(t *testing.T) {
	rendered := defaultVRFSection(t, renderFRR(t, true, &v1alpha1.NodeNetworkConfigSpec{
		GlobalWorkloadPorts: []v1alpha1.WorkloadPort{
			{Interface: "cra012345", HostRoutes: []string{"10.201.0.10/32", "fd00:201::10/128"}},
		},
	}))

	for _, want := range []string{
		"redistribute kernel route-map rm_workload_export",
		"address-family ipv6 unicast",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("default VRF config is missing %q, got:\n%s", want, rendered)
		}
	}
}

// The export must be restricted to the workload host routes themselves, so that
// redistributing kernel routes cannot leak node or link-local routes to the fabric.
func TestTemplateFRRRestrictsWorkloadExportToHostRoutes(t *testing.T) {
	rendered := renderFRR(t, true, &v1alpha1.NodeNetworkConfigSpec{
		GlobalWorkloadPorts: []v1alpha1.WorkloadPort{
			{Interface: "cra012345", HostRoutes: []string{"10.201.0.10/32", "fd00:201::10/128"}},
		},
	})

	for _, want := range []string{
		"ip prefix-list pl_workload_export permit 10.201.0.10/32",
		"ipv6 prefix-list pl_workload_export permit fd00:201::10/128",
		"match ip address prefix-list pl_workload_export",
		"match ipv6 address prefix-list pl_workload_export",
		"route-map rm_workload_export deny 65535",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("config is missing %q", want)
		}
	}
}

// Without workload ports nothing workload-related may appear at all.
func TestTemplateFRROmitsWorkloadExportWithoutPorts(t *testing.T) {
	rendered := renderFRR(t, false, &v1alpha1.NodeNetworkConfigSpec{})
	if strings.Contains(rendered, "workload_export") {
		t.Errorf("config unexpectedly references the workload export, got:\n%s", rendered)
	}
}

// Negotiating a fabric-facing IPv6 unicast AF on the EVPN VTEP changes the
// session capabilities, so it must stay opt-in via an IPv6 workload host route.
func TestTemplateFRROmitsUnderlayIPv6AFWithoutIPv6HostRoutes(t *testing.T) {
	for name, nodeConfig := range map[string]*v1alpha1.NodeNetworkConfigSpec{
		"no workload ports": {},
		"only IPv4 host routes": {
			GlobalWorkloadPorts: []v1alpha1.WorkloadPort{
				{Interface: "cra012345", HostRoutes: []string{"10.201.0.10/32"}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			rendered := defaultVRFSection(t, renderFRR(t, false, nodeConfig))
			if strings.Contains(rendered, "address-family ipv6 unicast") {
				t.Errorf("default VRF unexpectedly enables IPv6 unicast, got:\n%s", rendered)
			}
		})
	}
}

// The CRA shares its local ASN with the DCGWs, so fabric-originated routes look
// like an AS-path loop. The IPv4 AF relaxes that with allowas-in; without the
// same relaxation the IPv6 AF accepts no fabric route at all and the underlay
// return path towards a workload port stays broken.
func TestTemplateFRRAllowsOwnASNOnUnderlayIPv6(t *testing.T) {
	rendered := defaultVRFSection(t, renderFRR(t, true, &v1alpha1.NodeNetworkConfigSpec{
		GlobalWorkloadPorts: []v1alpha1.WorkloadPort{
			{Interface: "cra012345", HostRoutes: []string{"10.201.0.10/32", "fd00:201::10/128"}},
		},
	}))

	_, ipv6AF, found := strings.Cut(rendered, "address-family ipv6 unicast")
	if !found {
		t.Fatalf("default VRF has no IPv6 unicast AF, got:\n%s", rendered)
	}

	ipv6AF, _, _ = strings.Cut(ipv6AF, "exit-address-family")
	if !strings.Contains(ipv6AF, "allowas-in") {
		t.Errorf("underlay IPv6 AF does not allow the local ASN in the AS path, got:\n%s", ipv6AF)
	}
}

// A route-map clause must never reference a prefix-list that was not rendered:
// with only one address family in use, the other family's match clause has no
// prefix-list behind it and must be omitted entirely.
func TestTemplateFRRWorkloadExportOnlyMatchesRenderedFamilies(t *testing.T) {
	for _, tc := range []struct {
		name           string
		hostRoute      string
		wantMatch      string
		unwantedMatch  string
		unwantedPrefix string
	}{
		{
			name:           "ipv4 only",
			hostRoute:      "10.201.0.10/32",
			wantMatch:      "match ip address prefix-list pl_workload_export",
			unwantedMatch:  "match ipv6 address prefix-list pl_workload_export",
			unwantedPrefix: "ipv6 prefix-list pl_workload_export",
		},
		{
			name:           "ipv6 only",
			hostRoute:      "fd00:201::10/128",
			wantMatch:      "match ipv6 address prefix-list pl_workload_export",
			unwantedMatch:  "match ip address prefix-list pl_workload_export",
			unwantedPrefix: "ip prefix-list pl_workload_export",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderFRR(t, true, &v1alpha1.NodeNetworkConfigSpec{
				GlobalWorkloadPorts: []v1alpha1.WorkloadPort{
					{Interface: "cra012345", HostRoutes: []string{tc.hostRoute}},
				},
			})

			if !strings.Contains(rendered, tc.wantMatch) {
				t.Errorf("config is missing %q", tc.wantMatch)
			}
			if strings.Contains(rendered, tc.unwantedMatch) {
				t.Errorf("config matches %q but never renders the prefix-list for it", tc.unwantedMatch)
			}
			if strings.Contains(rendered, tc.unwantedPrefix+" permit") {
				t.Errorf("config renders %q for an address family that has no host routes", tc.unwantedPrefix)
			}
		})
	}
}
