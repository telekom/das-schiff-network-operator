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

package builder

import (
	"context"
	"strings"
	"testing"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptrString(s string) *string { return &s }
func ptrInt32(i int32) *int32    { return &i }

func baseSBRData() *resolver.ResolvedData {
	return &resolver.ResolvedData{
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		},
		VRFs: map[string]*resolver.ResolvedVRF{
			"internet-vrf": {Name: "internet-vrf", Spec: nc.VRFSpec{VRF: "internet", VNI: ptrInt32(1000)}},
		},
		Networks: map[string]*resolver.ResolvedNetwork{
			"svc-net": {Name: "svc-net", Spec: nc.NetworkSpec{
				IPv4: &nc.IPNetwork{CIDR: "198.51.100.0/24"},
			}},
			"pod-net": {Name: "pod-net", Spec: nc.NetworkSpec{
				IPv4: &nc.IPNetwork{CIDR: "10.244.0.0/16"},
			}},
		},
		Destinations: map[string]*resolver.ResolvedDestination{
			"ext-dest": {
				Name:    "ext-dest",
				Spec:    nc.DestinationSpec{VRFRef: ptrString("internet-vrf"), Prefixes: []string{"0.0.0.0/0"}},
				VRFSpec: &nc.VRFSpec{VRF: "internet", VNI: ptrInt32(1000)},
			},
		},
		RawDestinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "ext-dest", Labels: map[string]string{"role": "external"}},
				Spec:       nc.DestinationSpec{VRFRef: ptrString("internet-vrf"), Prefixes: []string{"0.0.0.0/0"}},
			},
		},
	}
}

func TestSBRBuilder_BasicOutbound(t *testing.T) {
	data := baseSBRData()
	data.Outbounds = []nc.Outbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "egress"},
			Spec: nc.OutboundSpec{
				NetworkRef:   "svc-net",
				Addresses:    &nc.AddressAllocation{IPv4: []string{"198.51.100.10"}},
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "external"}},
			},
		},
	}

	b := NewSBRBuilder()
	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)
	require.Contains(t, result, "worker-1")

	contrib := result["worker-1"]

	// Should have intermediate LocalVRF s-internet.
	require.Contains(t, contrib.LocalVRFs, "s-internet")
	localVRF := contrib.LocalVRFs["s-internet"]

	// Cluster VRF import for internal reachability.
	require.Len(t, localVRF.VRFImports, 1)
	assert.Equal(t, "cluster", localVRF.VRFImports[0].FromVRF)
	assert.Equal(t, networkv1alpha1.Accept, localVRF.VRFImports[0].Filter.DefaultAction.Type)

	// Static route to destination prefix via FabricVRF.
	require.Len(t, localVRF.StaticRoutes, 1)
	assert.Equal(t, "0.0.0.0/0", localVRF.StaticRoutes[0].Prefix)
	require.NotNil(t, localVRF.StaticRoutes[0].NextHop)
	assert.Equal(t, "internet", *localVRF.StaticRoutes[0].NextHop.Vrf)

	// ClusterVRF PolicyRoute: source-only (single VRF consumer).
	require.NotNil(t, contrib.ClusterVRF)
	require.Len(t, contrib.ClusterVRF.PolicyRoutes, 1)
	pr := contrib.ClusterVRF.PolicyRoutes[0]
	assert.Equal(t, "198.51.100.10/32", *pr.TrafficMatch.SrcPrefix)
	assert.Nil(t, pr.TrafficMatch.DstPrefix, "single-VRF should use source-only matching")
	assert.Equal(t, "s-internet", *pr.NextHop.Vrf)
}

func TestSBRBuilder_InboundWithStatusAddresses(t *testing.T) {
	data := baseSBRData()
	data.Inbounds = []nc.Inbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "my-vip"},
			Spec: nc.InboundSpec{
				NetworkRef:   "svc-net",
				Count:        ptrInt32(2),
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "external"}},
			},
			Status: nc.InboundStatus{
				Addresses: &nc.AddressAllocation{IPv4: []string{"198.51.100.1", "198.51.100.2"}},
			},
		},
	}

	b := NewSBRBuilder()
	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)

	contrib := result["worker-1"]
	require.NotNil(t, contrib.ClusterVRF)
	// Two PolicyRoutes — one per allocated address.
	require.Len(t, contrib.ClusterVRF.PolicyRoutes, 2)
	assert.Equal(t, "198.51.100.1/32", *contrib.ClusterVRF.PolicyRoutes[0].TrafficMatch.SrcPrefix)
	assert.Equal(t, "198.51.100.2/32", *contrib.ClusterVRF.PolicyRoutes[1].TrafficMatch.SrcPrefix)
}

func TestSBRBuilder_PodNetworkUsesNetworkCIDR(t *testing.T) {
	data := baseSBRData()
	data.PodNetworks = []nc.PodNetwork{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Spec: nc.PodNetworkSpec{
				NetworkRef:   "pod-net",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "external"}},
			},
		},
	}

	b := NewSBRBuilder()
	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)

	contrib := result["worker-1"]
	require.NotNil(t, contrib.ClusterVRF)
	require.Len(t, contrib.ClusterVRF.PolicyRoutes, 1)
	// Source should be the Network CIDR, not individual addresses.
	assert.Equal(t, "10.244.0.0/16", *contrib.ClusterVRF.PolicyRoutes[0].TrafficMatch.SrcPrefix)
}

func TestSBRBuilder_MultiVRFConsumer(t *testing.T) {
	data := baseSBRData()

	// Add a second VRF + destination.
	data.VRFs["private-vrf"] = &resolver.ResolvedVRF{
		Name: "private-vrf", Spec: nc.VRFSpec{VRF: "private", VNI: ptrInt32(2000)},
	}
	data.Destinations["priv-dest"] = &resolver.ResolvedDestination{
		Name:    "priv-dest",
		Spec:    nc.DestinationSpec{VRFRef: ptrString("private-vrf"), Prefixes: []string{"10.0.0.0/8"}},
		VRFSpec: &nc.VRFSpec{VRF: "private", VNI: ptrInt32(2000)},
	}
	data.RawDestinations = append(data.RawDestinations, nc.Destination{
		ObjectMeta: metav1.ObjectMeta{Name: "priv-dest", Labels: map[string]string{"role": "external", "zone": "private"}},
		Spec:       nc.DestinationSpec{VRFRef: ptrString("private-vrf"), Prefixes: []string{"10.0.0.0/8"}},
	})

	// Outbound selects BOTH destinations (role=external matches both).
	data.Outbounds = []nc.Outbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "egress"},
			Spec: nc.OutboundSpec{
				NetworkRef:   "svc-net",
				Addresses:    &nc.AddressAllocation{IPv4: []string{"198.51.100.10"}},
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "external"}},
			},
		},
	}

	b := NewSBRBuilder()
	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)

	contrib := result["worker-1"]

	// Multi-VRF consumer → ONE combo intermediate VRF (hashed name).
	require.Len(t, contrib.LocalVRFs, 1)

	// Find the combo VRF (name starts with "s-" and is a hash).
	var comboName string
	var comboVRF networkv1alpha1.VRF
	for name, vrf := range contrib.LocalVRFs {
		comboName = name
		comboVRF = vrf
	}
	assert.True(t, strings.HasPrefix(comboName, "s-"), "combo VRF name should start with s-")

	// Combo VRF has static routes to BOTH fabric VRFs (LPM does disambiguation).
	require.Len(t, comboVRF.StaticRoutes, 2)
	routesByPrefix := map[string]string{}
	for _, sr := range comboVRF.StaticRoutes {
		routesByPrefix[sr.Prefix] = *sr.NextHop.Vrf
	}
	assert.Equal(t, "internet", routesByPrefix["0.0.0.0/0"])
	assert.Equal(t, "private", routesByPrefix["10.0.0.0/8"])

	// ClusterVRF: source-only policy routes (no dst matching needed — LPM handles it).
	require.NotNil(t, contrib.ClusterVRF)
	require.Len(t, contrib.ClusterVRF.PolicyRoutes, 1)
	pr := contrib.ClusterVRF.PolicyRoutes[0]
	assert.Equal(t, "198.51.100.10/32", *pr.TrafficMatch.SrcPrefix)
	assert.Nil(t, pr.TrafficMatch.DstPrefix, "combo VRF uses LPM, not dst matching in policy routes")
	assert.Equal(t, comboName, *pr.NextHop.Vrf)
}

// Two consumers selecting the same destination set share a single combo VRF.
func TestSBRBuilder_MultiVRFComboDedup(t *testing.T) {
	data := baseSBRData()

	// Add a second VRF + destination.
	data.VRFs["private-vrf"] = &resolver.ResolvedVRF{
		Name: "private-vrf", Spec: nc.VRFSpec{VRF: "private", VNI: ptrInt32(2000)},
	}
	data.Destinations["priv-dest"] = &resolver.ResolvedDestination{
		Name:    "priv-dest",
		Spec:    nc.DestinationSpec{VRFRef: ptrString("private-vrf"), Prefixes: []string{"10.0.0.0/8"}},
		VRFSpec: &nc.VRFSpec{VRF: "private", VNI: ptrInt32(2000)},
	}
	data.RawDestinations = append(data.RawDestinations, nc.Destination{
		ObjectMeta: metav1.ObjectMeta{Name: "priv-dest", Labels: map[string]string{"role": "external", "zone": "private"}},
		Spec:       nc.DestinationSpec{VRFRef: ptrString("private-vrf"), Prefixes: []string{"10.0.0.0/8"}},
	})

	// Two different consumers both select role=external (same destination set).
	data.Inbounds = []nc.Inbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "lb-vip"},
			Spec: nc.InboundSpec{
				NetworkRef:   "svc-net",
				Addresses:    &nc.AddressAllocation{IPv4: []string{"198.51.100.1"}},
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "external"}},
			},
		},
	}
	data.Outbounds = []nc.Outbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "egress"},
			Spec: nc.OutboundSpec{
				NetworkRef:   "svc-net",
				Addresses:    &nc.AddressAllocation{IPv4: []string{"198.51.100.10"}},
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "external"}},
			},
		},
	}

	b := NewSBRBuilder()
	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)

	contrib := result["worker-1"]

	// Same destination set → ONE shared combo VRF, with BOTH source prefixes.
	require.Len(t, contrib.LocalVRFs, 1)
	require.Len(t, contrib.ClusterVRF.PolicyRoutes, 2)

	sources := map[string]bool{}
	for _, pr := range contrib.ClusterVRF.PolicyRoutes {
		sources[*pr.TrafficMatch.SrcPrefix] = true
		assert.Nil(t, pr.TrafficMatch.DstPrefix, "combo VRF — no dst matching needed")
	}
	assert.True(t, sources["198.51.100.1/32"])
	assert.True(t, sources["198.51.100.10/32"])
}

func TestSBRBuilder_MultiConsumerMerge(t *testing.T) {
	data := baseSBRData()

	// Both Inbound and Outbound target same VRF.
	data.Inbounds = []nc.Inbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "vip"},
			Spec: nc.InboundSpec{
				NetworkRef:   "svc-net",
				Addresses:    &nc.AddressAllocation{IPv4: []string{"198.51.100.1"}},
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "external"}},
			},
		},
	}
	data.Outbounds = []nc.Outbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "egress"},
			Spec: nc.OutboundSpec{
				NetworkRef:   "svc-net",
				Addresses:    &nc.AddressAllocation{IPv4: []string{"198.51.100.10"}},
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "external"}},
			},
		},
	}

	b := NewSBRBuilder()
	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)

	contrib := result["worker-1"]

	// Only ONE intermediate VRF (same destination VRF).
	require.Len(t, contrib.LocalVRFs, 1)
	require.Contains(t, contrib.LocalVRFs, "s-internet")

	// TWO PolicyRoutes (one per source prefix, merged from both consumers).
	require.Len(t, contrib.ClusterVRF.PolicyRoutes, 2)
	sources := map[string]bool{}
	for _, pr := range contrib.ClusterVRF.PolicyRoutes {
		sources[*pr.TrafficMatch.SrcPrefix] = true
	}
	assert.True(t, sources["198.51.100.1/32"])
	assert.True(t, sources["198.51.100.10/32"])
}

func TestSBRBuilder_NoDestinationSelector(t *testing.T) {
	data := baseSBRData()
	data.Outbounds = []nc.Outbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "egress"},
			Spec: nc.OutboundSpec{
				NetworkRef: "svc-net",
				Addresses:  &nc.AddressAllocation{IPv4: []string{"198.51.100.10"}},
				// No Destinations selector — no SBR needed.
			},
		},
	}

	b := NewSBRBuilder()
	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)
	assert.Nil(t, result, "no SBR output when no destination selector")
}

func TestSBRBuilder_NextHopDestinationSkipped(t *testing.T) {
	data := baseSBRData()

	// Replace the VRF-backed destination with a nextHop-based one.
	data.RawDestinations = []nc.Destination{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "static-dest", Labels: map[string]string{"role": "static"}},
			Spec:       nc.DestinationSpec{NextHop: &nc.NextHopConfig{IPv4: ptrString("10.0.0.1")}},
		},
	}
	data.Destinations = map[string]*resolver.ResolvedDestination{
		"static-dest": {Name: "static-dest", Spec: nc.DestinationSpec{NextHop: &nc.NextHopConfig{IPv4: ptrString("10.0.0.1")}}},
	}
	data.Outbounds = []nc.Outbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "egress"},
			Spec: nc.OutboundSpec{
				NetworkRef:   "svc-net",
				Addresses:    &nc.AddressAllocation{IPv4: []string{"198.51.100.10"}},
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "static"}},
			},
		},
	}

	b := NewSBRBuilder()
	result, err := b.Build(context.Background(), data)
	require.NoError(t, err)
	assert.Nil(t, result, "nextHop destinations should not trigger SBR")
}

func TestEnsureCIDR(t *testing.T) {
	assert.Equal(t, "10.0.0.1/32", ensureCIDR("10.0.0.1", "/32"))
	assert.Equal(t, "10.0.0.0/24", ensureCIDR("10.0.0.0/24", "/32"))
	assert.Equal(t, "fd00::1/128", ensureCIDR("fd00::1", "/128"))
}

func TestAppendUnique(t *testing.T) {
	result := appendUnique([]string{"a", "b"}, "b", "c", "a")
	assert.Equal(t, []string{"a", "b", "c"}, result)
}
