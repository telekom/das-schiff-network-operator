// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
// SPDX-License-Identifier: Apache-2.0
//
// These tests preserve persisted gateway outputs across library adoption.

package intent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8syaml "sigs.k8s.io/yaml"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/builder"
)

func TestGatewayAdoptionIntentOutputs(t *testing.T) {
	tests := []struct {
		name       string
		ipv4, ipv6 string
		gateways   []string
		irb        []string
		addresses  nc.AddressAllocation
		nodeCIDRs  []string
	}{
		{"dual", "198.51.100.224/27", "2001:db8::/64",
			[]string{"198.51.100.225", "2001:db8::1"},
			[]string{"198.51.100.225/27", "2001:db8::1/64"},
			nc.AddressAllocation{IPv4: []string{"198.51.100.230"}, IPv6: []string{"2001:db8::30"}},
			[]string{"198.51.100.230/27", "2001:db8::30/64"}},
		{"point-to-point", "192.0.2.0/31", "2001:db8::/127",
			[]string{"192.0.2.0", "2001:db8::"},
			[]string{"192.0.2.0/31", "2001:db8::/127"},
			nc.AddressAllocation{IPv4: []string{"192.0.2.1"}, IPv6: []string{"2001:db8::1"}},
			[]string{"192.0.2.1/31", "2001:db8::1/127"}},
		{"ipv4-single", "192.0.2.5/32", "", nil, nil, nc.AddressAllocation{}, nil},
		{"ipv6-single", "", "2001:db8::5/128", nil, nil, nc.AddressAllocation{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			name := "gateway-adoption-" + tt.name
			selector := &metav1.LabelSelector{MatchLabels: map[string]string{"gateway-adoption": name}}
			createNode(t, ctx, name, selector.MatchLabels)
			createObj(t, ctx, makeVRF(name, "gwtest", 100149, "65000:149"))
			createObj(t, ctx, makeNetwork(name, 149, 100149, tt.ipv4, tt.ipv6))
			createObj(t, ctx, makeDestination(name, name, map[string]string{"type": name}, nil))
			l2a := makeL2A(name, name, destSelector(name), selector)
			if tt.gateways != nil {
				l2a.Spec.NodeIPs = &nc.NodeIPConfig{Enabled: true}
			}
			createObj(t, ctx, l2a)
			if tt.gateways != nil {
				l2a.Status.NodeAddresses = map[string]nc.AddressAllocation{name: tt.addresses}
				require.NoError(t, k8sClient.Status().Update(ctx, l2a))
			}
			bgp := &nc.BGPPeering{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
				Spec: nc.BGPPeeringSpec{
					Mode: nc.BGPPeeringModeListenRange,
					Ref: nc.BGPPeeringRef{
						AttachmentRef: ptr(name), NetworkRefs: []string{name},
					},
					WorkloadAS: ptr(int64(65100)),
				},
			}
			createObj(t, ctx, bgp)
			require.NoError(t, reconciler.ReconcileDebounced(ctx))
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(bgp), bgp))
			assert.Equal(t, tt.gateways, bgp.Status.LocalIPs)
			nnc := reconcileAndGetNNC(t, ctx, name)
			netplan := getNetplanConfig(t, ctx, name)
			if tt.gateways == nil {
				require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(l2a), l2a))
				ready := apimeta.FindStatusCondition(l2a.Status.Conditions, "Ready")
				require.NotNil(t, ready)
				assert.Equal(t, metav1.ConditionFalse, ready.Status)
				assert.Equal(t, "InvalidIRBGateway", ready.Reason)
				assert.NotContains(t, nnc.Spec.Layer2s, "149")
				assert.NotContains(t, netplan.Spec.DesiredState.Network.VLans, "vlan.149")
				return
			}
			require.NotNil(t, nnc.Spec.Layer2s["149"].IRB)
			assert.Equal(t, tt.irb, nnc.Spec.Layer2s["149"].IRB.IPAddresses)
			var vlan struct {
				Addresses []string               `json:"addresses"`
				Routes    []builder.NetplanRoute `json:"routes"`
			}
			require.NoError(t, k8syaml.Unmarshal(netplan.Spec.DesiredState.Network.VLans["vlan.149"].Raw, &vlan))
			require.Len(t, vlan.Routes, len(tt.gateways))
			for i, route := range vlan.Routes {
				assert.Equal(t, "default", route.To)
				assert.Equal(t, tt.gateways[i], route.Via)
			}
			assert.Equal(t, tt.nodeCIDRs, vlan.Addresses)
		})
	}
}
