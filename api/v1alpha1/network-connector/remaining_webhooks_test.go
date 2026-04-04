/*
Copyright 2022.

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

package networkconnector

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// ---------------------------------------------------------------------------
// helpers (int32Ptr and strPtr live in network_vrf_webhook_test.go)
// ---------------------------------------------------------------------------



// ===========================================================================
// BGPPeering tests
// ===========================================================================

func TestBGPPeeringValidateCreate_ListenRange_Valid(t *testing.T) {
	r := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeListenRange,
		Ref: BGPPeeringRef{
			AttachmentRef: strPtr("l2a-1"),
			InboundRefs:   []string{"ib-1"},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBGPPeeringValidateCreate_LoopbackPeer_Valid(t *testing.T) {
	r := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeLoopbackPeer,
		Ref: BGPPeeringRef{
			InboundRefs: []string{"ib-1", "ib-2"},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBGPPeeringValidateCreate_EmptyInboundRefs(t *testing.T) {
	r := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeListenRange,
		Ref: BGPPeeringRef{
			AttachmentRef: strPtr("l2a-1"),
			InboundRefs:   []string{},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for empty inboundRefs, got nil")
	}
}

func TestBGPPeeringValidateCreate_NilInboundRefs(t *testing.T) {
	r := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeListenRange,
		Ref: BGPPeeringRef{
			AttachmentRef: strPtr("l2a-1"),
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for nil inboundRefs, got nil")
	}
}

func TestBGPPeeringValidateCreate_ListenRange_MissingAttachmentRef(t *testing.T) {
	r := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeListenRange,
		Ref: BGPPeeringRef{
			InboundRefs: []string{"ib-1"},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for listenRange without attachmentRef, got nil")
	}
}

func TestBGPPeeringValidateCreate_LoopbackPeer_WithAttachmentRef(t *testing.T) {
	r := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeLoopbackPeer,
		Ref: BGPPeeringRef{
			AttachmentRef: strPtr("l2a-1"),
			InboundRefs:   []string{"ib-1"},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for loopbackPeer with attachmentRef, got nil")
	}
}

func TestBGPPeeringValidateCreate_InvalidMode(t *testing.T) {
	r := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringMode("invalid"),
		Ref: BGPPeeringRef{
			InboundRefs: []string{"ib-1"},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
}

func TestBGPPeeringValidateUpdate_ModeImmutable(t *testing.T) {
	old := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeListenRange,
		Ref: BGPPeeringRef{
			AttachmentRef: strPtr("l2a-1"),
			InboundRefs:   []string{"ib-1"},
		},
	}}
	r := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeLoopbackPeer,
		Ref: BGPPeeringRef{
			InboundRefs: []string{"ib-1"},
		},
	}}
	if _, err := r.ValidateUpdate(context.Background(), old, r); err == nil {
		t.Fatal("expected error for changed mode, got nil")
	}
}

func TestBGPPeeringValidateUpdate_ModeUnchanged(t *testing.T) {
	old := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeLoopbackPeer,
		Ref: BGPPeeringRef{
			InboundRefs: []string{"ib-1"},
		},
	}}
	r := &BGPPeering{Spec: BGPPeeringSpec{
		Mode: BGPPeeringModeLoopbackPeer,
		Ref: BGPPeeringRef{
			InboundRefs: []string{"ib-1", "ib-2"},
		},
	}}
	if _, err := r.ValidateUpdate(context.Background(), old, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBGPPeeringValidateDelete_AlwaysSucceeds(t *testing.T) {
	r := &BGPPeering{}
	if _, err := r.ValidateDelete(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ===========================================================================
// PodNetwork tests
// ===========================================================================

func TestPodNetworkValidateCreate_Valid(t *testing.T) {
	r := &PodNetwork{Spec: PodNetworkSpec{NetworkRef: "net-1"}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPodNetworkValidateCreate_EmptyNetworkRef(t *testing.T) {
	r := &PodNetwork{Spec: PodNetworkSpec{NetworkRef: ""}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for empty networkRef, got nil")
	}
}

func TestPodNetworkValidateUpdate_Valid(t *testing.T) {
	old := &PodNetwork{Spec: PodNetworkSpec{NetworkRef: "net-1"}}
	r := &PodNetwork{Spec: PodNetworkSpec{NetworkRef: "net-1"}}
	if _, err := r.ValidateUpdate(context.Background(), old, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPodNetworkValidateUpdate_NetworkRefImmutable(t *testing.T) {
	old := &PodNetwork{Spec: PodNetworkSpec{NetworkRef: "net-1"}}
	r := &PodNetwork{Spec: PodNetworkSpec{NetworkRef: "net-2"}}
	if _, err := r.ValidateUpdate(context.Background(), old, r); err == nil {
		t.Fatal("expected error for changed networkRef, got nil")
	}
}

func TestPodNetworkValidateDelete_AlwaysSucceeds(t *testing.T) {
	r := &PodNetwork{}
	if _, err := r.ValidateDelete(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ===========================================================================
// Collector tests
// ===========================================================================

func validCollector() *Collector {
	return &Collector{Spec: CollectorSpec{
		Address:  "10.0.0.1",
		Protocol: "l3gre",
		MirrorVRF: MirrorVRFRef{
			Name: "mirror-vrf",
			Loopback: LoopbackConfig{
				Name: "lo.mir",
				PoolRef: corev1.TypedLocalObjectReference{
					APIGroup: strPtr("ipam.cluster.x-k8s.io"),
					Kind:     "InClusterIPPool",
					Name:     "pool-1",
				},
			},
		},
	}}
}

func TestCollectorValidateCreate_Valid(t *testing.T) {
	r := validCollector()
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCollectorValidateCreate_IPv6Address(t *testing.T) {
	r := validCollector()
	r.Spec.Address = "2001:db8::1"
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCollectorValidateCreate_EmptyAddress(t *testing.T) {
	r := validCollector()
	r.Spec.Address = ""
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for empty address, got nil")
	}
}

func TestCollectorValidateCreate_InvalidAddress(t *testing.T) {
	r := validCollector()
	r.Spec.Address = "not-an-ip"
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for invalid IP address, got nil")
	}
}

func TestCollectorValidateCreate_EmptyMirrorVRFName(t *testing.T) {
	r := validCollector()
	r.Spec.MirrorVRF.Name = ""
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for empty mirrorVRF.name, got nil")
	}
}

func TestCollectorValidateCreate_EmptyLoopbackName(t *testing.T) {
	r := validCollector()
	r.Spec.MirrorVRF.Loopback.Name = ""
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for empty mirrorVRF.loopback.name, got nil")
	}
}

func TestCollectorValidateUpdate_ProtocolImmutable(t *testing.T) {
	old := validCollector()
	r := validCollector()
	r.Spec.Protocol = "l2gre"
	if _, err := r.ValidateUpdate(context.Background(), old, r); err == nil {
		t.Fatal("expected error for changed protocol, got nil")
	}
}

func TestCollectorValidateUpdate_ProtocolUnchanged(t *testing.T) {
	old := validCollector()
	r := validCollector()
	r.Spec.Address = "10.0.0.2"
	if _, err := r.ValidateUpdate(context.Background(), old, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCollectorValidateDelete_AlwaysSucceeds(t *testing.T) {
	r := &Collector{}
	if _, err := r.ValidateDelete(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ===========================================================================
// TrafficMirror tests
// ===========================================================================

func TestTrafficMirrorValidateCreate_Valid(t *testing.T) {
	r := &TrafficMirror{Spec: TrafficMirrorSpec{
		Source:    MirrorSource{Kind: "Inbound", Name: "ib-1"},
		Collector: "collector-1",
		Direction: "ingress",
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTrafficMirrorValidateCreate_WithTrafficMatch(t *testing.T) {
	r := &TrafficMirror{Spec: TrafficMirrorSpec{
		Source:    MirrorSource{Kind: "Inbound", Name: "ib-1"},
		Collector: "collector-1",
		Direction: "both",
		TrafficMatch: &TrafficMatch{
			SrcPrefix: strPtr("10.0.0.0/24"),
			DstPrefix: strPtr("192.168.1.0/24"),
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTrafficMirrorValidateCreate_EmptySourceName(t *testing.T) {
	r := &TrafficMirror{Spec: TrafficMirrorSpec{
		Source:    MirrorSource{Kind: "Inbound", Name: ""},
		Collector: "collector-1",
		Direction: "ingress",
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for empty source.name, got nil")
	}
}

func TestTrafficMirrorValidateCreate_EmptyCollector(t *testing.T) {
	r := &TrafficMirror{Spec: TrafficMirrorSpec{
		Source:    MirrorSource{Kind: "Inbound", Name: "ib-1"},
		Collector: "",
		Direction: "ingress",
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for empty collector, got nil")
	}
}

func TestTrafficMirrorValidateCreate_InvalidSrcPrefix(t *testing.T) {
	r := &TrafficMirror{Spec: TrafficMirrorSpec{
		Source:    MirrorSource{Kind: "Inbound", Name: "ib-1"},
		Collector: "collector-1",
		Direction: "ingress",
		TrafficMatch: &TrafficMatch{
			SrcPrefix: strPtr("not-a-cidr"),
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for invalid srcPrefix, got nil")
	}
}

func TestTrafficMirrorValidateCreate_InvalidDstPrefix(t *testing.T) {
	r := &TrafficMirror{Spec: TrafficMirrorSpec{
		Source:    MirrorSource{Kind: "Inbound", Name: "ib-1"},
		Collector: "collector-1",
		Direction: "ingress",
		TrafficMatch: &TrafficMatch{
			DstPrefix: strPtr("xyz"),
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for invalid dstPrefix, got nil")
	}
}

func TestTrafficMirrorValidateUpdate_Valid(t *testing.T) {
	old := &TrafficMirror{Spec: TrafficMirrorSpec{
		Source:    MirrorSource{Kind: "Inbound", Name: "ib-1"},
		Collector: "collector-1",
		Direction: "ingress",
	}}
	r := &TrafficMirror{Spec: TrafficMirrorSpec{
		Source:    MirrorSource{Kind: "Inbound", Name: "ib-1"},
		Collector: "collector-1",
		Direction: "egress",
	}}
	if _, err := r.ValidateUpdate(context.Background(), old, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTrafficMirrorValidateDelete_AlwaysSucceeds(t *testing.T) {
	r := &TrafficMirror{}
	if _, err := r.ValidateDelete(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ===========================================================================
// AnnouncementPolicy tests
// ===========================================================================

func TestAnnouncementPolicyValidateCreate_Valid(t *testing.T) {
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{VRFRef: "vrf-1"}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnnouncementPolicyValidateCreate_WithAggregate(t *testing.T) {
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{
		VRFRef: "vrf-1",
		Aggregate: &AggregateConfig{
			PrefixLengthV4: int32Ptr(24),
			PrefixLengthV6: int32Ptr(64),
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnnouncementPolicyValidateCreate_EmptyVRFRef(t *testing.T) {
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{VRFRef: ""}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for empty vrfRef, got nil")
	}
}

func TestAnnouncementPolicyValidateCreate_PrefixLengthV4Zero(t *testing.T) {
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{
		VRFRef:    "vrf-1",
		Aggregate: &AggregateConfig{PrefixLengthV4: int32Ptr(0)},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for prefixLengthV4=0, got nil")
	}
}

func TestAnnouncementPolicyValidateCreate_PrefixLengthV4TooHigh(t *testing.T) {
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{
		VRFRef:    "vrf-1",
		Aggregate: &AggregateConfig{PrefixLengthV4: int32Ptr(33)},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for prefixLengthV4=33, got nil")
	}
}

func TestAnnouncementPolicyValidateCreate_PrefixLengthV6Zero(t *testing.T) {
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{
		VRFRef:    "vrf-1",
		Aggregate: &AggregateConfig{PrefixLengthV6: int32Ptr(0)},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for prefixLengthV6=0, got nil")
	}
}

func TestAnnouncementPolicyValidateCreate_PrefixLengthV6TooHigh(t *testing.T) {
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{
		VRFRef:    "vrf-1",
		Aggregate: &AggregateConfig{PrefixLengthV6: int32Ptr(129)},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for prefixLengthV6=129, got nil")
	}
}

func TestAnnouncementPolicyValidateCreate_PrefixLengthV4Boundary(t *testing.T) {
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{
		VRFRef:    "vrf-1",
		Aggregate: &AggregateConfig{PrefixLengthV4: int32Ptr(32)},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error for prefixLengthV4=32: %v", err)
	}
}

func TestAnnouncementPolicyValidateCreate_PrefixLengthV6Boundary(t *testing.T) {
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{
		VRFRef:    "vrf-1",
		Aggregate: &AggregateConfig{PrefixLengthV6: int32Ptr(128)},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error for prefixLengthV6=128: %v", err)
	}
}

func TestAnnouncementPolicyValidateUpdate_Valid(t *testing.T) {
	old := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{VRFRef: "vrf-1"}}
	r := &AnnouncementPolicy{Spec: AnnouncementPolicySpec{
		VRFRef:    "vrf-1",
		Aggregate: &AggregateConfig{PrefixLengthV4: int32Ptr(24)},
	}}
	if _, err := r.ValidateUpdate(context.Background(), old, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnnouncementPolicyValidateDelete_AlwaysSucceeds(t *testing.T) {
	r := &AnnouncementPolicy{}
	if _, err := r.ValidateDelete(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ===========================================================================
// Destination tests
// ===========================================================================

func TestDestinationValidateCreate_WithVRFRef(t *testing.T) {
	r := &Destination{Spec: DestinationSpec{
		VRFRef:   strPtr("vrf-1"),
		Prefixes: []string{"10.0.0.0/24", "192.168.0.0/16"},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDestinationValidateCreate_WithNextHopIPv4(t *testing.T) {
	r := &Destination{Spec: DestinationSpec{
		NextHop: &NextHopConfig{IPv4: strPtr("10.0.0.1")},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDestinationValidateCreate_WithNextHopIPv6(t *testing.T) {
	r := &Destination{Spec: DestinationSpec{
		NextHop: &NextHopConfig{IPv6: strPtr("2001:db8::1")},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDestinationValidateCreate_InvalidPrefix(t *testing.T) {
	r := &Destination{Spec: DestinationSpec{
		VRFRef:   strPtr("vrf-1"),
		Prefixes: []string{"10.0.0.0/24", "bad-cidr"},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for invalid CIDR prefix, got nil")
	}
}

func TestDestinationValidateCreate_InvalidNextHopIPv4(t *testing.T) {
	r := &Destination{Spec: DestinationSpec{
		NextHop: &NextHopConfig{IPv4: strPtr("not-an-ip")},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for invalid IPv4 next-hop, got nil")
	}
}

func TestDestinationValidateCreate_InvalidNextHopIPv6(t *testing.T) {
	r := &Destination{Spec: DestinationSpec{
		NextHop: &NextHopConfig{IPv6: strPtr("not-valid")},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for invalid IPv6 next-hop, got nil")
	}
}

func TestDestinationValidateUpdate_Valid(t *testing.T) {
	old := &Destination{Spec: DestinationSpec{VRFRef: strPtr("vrf-1")}}
	r := &Destination{Spec: DestinationSpec{
		VRFRef:   strPtr("vrf-1"),
		Prefixes: []string{"10.0.0.0/8"},
	}}
	if _, err := r.ValidateUpdate(context.Background(), old, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDestinationValidateDelete_AlwaysSucceeds(t *testing.T) {
	r := &Destination{}
	if _, err := r.ValidateDelete(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ===========================================================================
// InterfaceConfig tests
// ===========================================================================

func TestInterfaceConfigValidateCreate_EthernetsOnly(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Ethernets: map[string]EthernetConfig{
			"eth0": {Mtu: int32Ptr(1500)},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInterfaceConfigValidateCreate_BondsOnly(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Bonds: map[string]BondConfig{
			"bond0": {Interfaces: []string{"eth0", "eth1"}, Mtu: int32Ptr(9000)},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInterfaceConfigValidateCreate_BothEthernetsAndBonds(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Ethernets: map[string]EthernetConfig{
			"eth2": {},
		},
		Bonds: map[string]BondConfig{
			"bond0": {Interfaces: []string{"eth0", "eth1"}},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInterfaceConfigValidateCreate_NeitherEthernetsNorBonds(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for no ethernets or bonds, got nil")
	}
}

func TestInterfaceConfigValidateCreate_EmptyBondMember(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Bonds: map[string]BondConfig{
			"bond0": {Interfaces: []string{"eth0", ""}},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for empty bond member, got nil")
	}
}

func TestInterfaceConfigValidateCreate_EthernetMTUTooLow(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Ethernets: map[string]EthernetConfig{
			"eth0": {Mtu: int32Ptr(999)},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for ethernet MTU < 1000, got nil")
	}
}

func TestInterfaceConfigValidateCreate_EthernetMTUTooHigh(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Ethernets: map[string]EthernetConfig{
			"eth0": {Mtu: int32Ptr(9001)},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for ethernet MTU > 9000, got nil")
	}
}

func TestInterfaceConfigValidateCreate_BondMTUTooLow(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Bonds: map[string]BondConfig{
			"bond0": {Interfaces: []string{"eth0"}, Mtu: int32Ptr(500)},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for bond MTU < 1000, got nil")
	}
}

func TestInterfaceConfigValidateCreate_BondMTUTooHigh(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Bonds: map[string]BondConfig{
			"bond0": {Interfaces: []string{"eth0"}, Mtu: int32Ptr(10000)},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err == nil {
		t.Fatal("expected error for bond MTU > 9000, got nil")
	}
}

func TestInterfaceConfigValidateCreate_EthernetMTUBoundaryLow(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Ethernets: map[string]EthernetConfig{
			"eth0": {Mtu: int32Ptr(1000)},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error for MTU=1000: %v", err)
	}
}

func TestInterfaceConfigValidateCreate_BondMTUBoundaryHigh(t *testing.T) {
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Bonds: map[string]BondConfig{
			"bond0": {Interfaces: []string{"eth0"}, Mtu: int32Ptr(9000)},
		},
	}}
	if _, err := r.ValidateCreate(context.Background(), r); err != nil {
		t.Fatalf("unexpected error for bond MTU=9000: %v", err)
	}
}

func TestInterfaceConfigValidateUpdate_Valid(t *testing.T) {
	old := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Ethernets: map[string]EthernetConfig{"eth0": {}},
	}}
	r := &InterfaceConfig{Spec: InterfaceConfigSpec{
		Ethernets: map[string]EthernetConfig{"eth0": {Mtu: int32Ptr(1500)}},
	}}
	if _, err := r.ValidateUpdate(context.Background(), old, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInterfaceConfigValidateDelete_AlwaysSucceeds(t *testing.T) {
	r := &InterfaceConfig{}
	if _, err := r.ValidateDelete(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
