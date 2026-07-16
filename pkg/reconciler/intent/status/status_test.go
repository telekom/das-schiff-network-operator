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

package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

func TestEffectiveInterfaceName(t *testing.T) {
	vlan1000 := int32(1000)
	override := "chris-l2"
	empty := ""
	ifref := "eth1"

	resolved := &resolver.ResolvedData{
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-be":      {Name: "net-be", Spec: nc.NetworkSpec{VLAN: &vlan1000}},
			"net-no-vlan": {Name: "net-no-vlan", Spec: nc.NetworkSpec{}},
		},
	}

	tests := []struct {
		name string
		spec nc.Layer2AttachmentSpec
		want string
	}{
		{
			name: "no interfaceName defaults to vlan.<vlan from networkRef>",
			spec: nc.Layer2AttachmentSpec{NetworkRef: "net-be"},
			want: "vlan.1000",
		},
		{
			name: "interfaceName override wins over default",
			spec: nc.Layer2AttachmentSpec{NetworkRef: "net-be", InterfaceName: &override},
			want: "chris-l2",
		},
		{
			name: "empty interfaceName pointer falls back to default",
			spec: nc.Layer2AttachmentSpec{NetworkRef: "net-be", InterfaceName: &empty},
			want: "vlan.1000",
		},
		{
			name: "unresolved networkRef yields empty",
			spec: nc.Layer2AttachmentSpec{NetworkRef: "does-not-exist"},
			want: "",
		},
		{
			name: "network without VLAN and no interfaceRef yields empty",
			spec: nc.Layer2AttachmentSpec{NetworkRef: "net-no-vlan"},
			want: "",
		},
		{
			name: "native mode reports interfaceRef",
			spec: nc.Layer2AttachmentSpec{NetworkRef: "net-no-vlan", InterfaceRef: &ifref},
			want: "eth1",
		},
		{
			name: "native mode ignores interfaceName override",
			spec: nc.Layer2AttachmentSpec{NetworkRef: "net-no-vlan", InterfaceRef: &ifref, InterfaceName: &override},
			want: "eth1",
		},
		{
			name: "override still reported when networkRef unresolved",
			spec: nc.Layer2AttachmentSpec{NetworkRef: "does-not-exist", InterfaceName: &override},
			want: "chris-l2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l2a := &nc.Layer2Attachment{Spec: tc.spec}
			assert.Equal(t, tc.want, effectiveInterfaceName(l2a, resolved))
		})
	}
}

func TestCheckBGPPeeringRefs(t *testing.T) {
	attachment := "l2a-be"
	resolved := &resolver.ResolvedData{
		Networks: map[string]*resolver.ResolvedNetwork{
			"net-be": {Name: "net-be"},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-be"}},
		},
		Inbounds: []nc.Inbound{
			{ObjectMeta: metav1.ObjectMeta{Name: "inb-vip"}},
		},
	}

	tests := []struct {
		name       string
		spec       nc.BGPPeeringSpec
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name: "listenRange all references resolve",
			spec: nc.BGPPeeringSpec{
				Mode: nc.BGPPeeringModeListenRange,
				Ref:  nc.BGPPeeringRef{AttachmentRef: &attachment, NetworkRefs: []string{"net-be"}},
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: reasonAllResolved,
		},
		{
			name: "listenRange missing attachmentRef",
			spec: nc.BGPPeeringSpec{
				Mode: nc.BGPPeeringModeListenRange,
				Ref:  nc.BGPPeeringRef{NetworkRefs: []string{"net-be"}},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "AttachmentRefMissing",
		},
		{
			name: "listenRange unknown attachment",
			spec: nc.BGPPeeringSpec{
				Mode: nc.BGPPeeringModeListenRange,
				Ref:  nc.BGPPeeringRef{AttachmentRef: ptr("nope"), NetworkRefs: []string{"net-be"}},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "AttachmentNotFound",
		},
		{
			name: "listenRange unknown network",
			spec: nc.BGPPeeringSpec{
				Mode: nc.BGPPeeringModeListenRange,
				Ref:  nc.BGPPeeringRef{AttachmentRef: &attachment, NetworkRefs: []string{"missing"}},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "NetworkNotFound",
		},
		{
			name: "listenRange missing networkRefs",
			spec: nc.BGPPeeringSpec{
				Mode: nc.BGPPeeringModeListenRange,
				Ref:  nc.BGPPeeringRef{AttachmentRef: &attachment},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "NetworkRefsMissing",
		},
		{
			name: "loopbackPeer inbound resolves",
			spec: nc.BGPPeeringSpec{
				Mode: nc.BGPPeeringModeLoopbackPeer,
				Ref:  nc.BGPPeeringRef{InboundRefs: []string{"inb-vip"}},
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: reasonAllResolved,
		},
		{
			name: "loopbackPeer unknown inbound",
			spec: nc.BGPPeeringSpec{
				Mode: nc.BGPPeeringModeLoopbackPeer,
				Ref:  nc.BGPPeeringRef{InboundRefs: []string{"missing"}},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "InboundNotFound",
		},
		{
			name: "loopbackPeer missing inboundRefs",
			spec: nc.BGPPeeringSpec{
				Mode: nc.BGPPeeringModeLoopbackPeer,
				Ref:  nc.BGPPeeringRef{},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "InboundRefsMissing",
		},
		{
			name: "unknown mode is not resolved",
			spec: nc.BGPPeeringSpec{
				Mode: nc.BGPPeeringMode("bogus"),
				Ref:  nc.BGPPeeringRef{},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "UnknownMode",
		},
		{
			name: "empty mode is not resolved",
			spec: nc.BGPPeeringSpec{
				Ref: nc.BGPPeeringRef{},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "UnknownMode",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bp := &nc.BGPPeering{Spec: tc.spec}
			gotStatus, gotReason, _ := checkBGPPeeringRefs(bp, resolved)
			assert.Equal(t, tc.wantStatus, gotStatus)
			assert.Equal(t, tc.wantReason, gotReason)
		})
	}
}

func ptr(s string) *string { return &s }
