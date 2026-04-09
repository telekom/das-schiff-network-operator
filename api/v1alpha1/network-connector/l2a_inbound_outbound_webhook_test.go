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
)

// ---------------------------------------------------------------------------
// Layer2Attachment – valid cases
// ---------------------------------------------------------------------------

func TestLayer2AttachmentValidateCreate_Valid(t *testing.T) {
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{NetworkRef: "net-1"},
	}
	if _, err := l2a.ValidateCreate(context.Background(), l2a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLayer2AttachmentValidateCreate_WithMTU(t *testing.T) {
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef: "net-1",
			MTU:        int32Ptr(9000),
		},
	}
	if _, err := l2a.ValidateCreate(context.Background(), l2a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLayer2AttachmentValidateCreate_WithInterfaceName(t *testing.T) {
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef:    "net-1",
			InterfaceName: strPtr("eth0"),
		},
	}
	if _, err := l2a.ValidateCreate(context.Background(), l2a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLayer2AttachmentValidateUpdate_Valid(t *testing.T) {
	old := &Layer2Attachment{Spec: Layer2AttachmentSpec{NetworkRef: "net-1"}}
	l2a := &Layer2Attachment{Spec: Layer2AttachmentSpec{NetworkRef: "net-1", MTU: int32Ptr(1500)}}
	if _, err := l2a.ValidateUpdate(context.Background(), old, l2a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLayer2AttachmentValidateDelete_AlwaysSucceeds(t *testing.T) {
	l2a := &Layer2Attachment{}
	if _, err := l2a.ValidateDelete(context.Background(), l2a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Layer2Attachment – invalid cases
// ---------------------------------------------------------------------------

func TestLayer2AttachmentValidateCreate_EmptyNetworkRef(t *testing.T) {
	l2a := &Layer2Attachment{Spec: Layer2AttachmentSpec{NetworkRef: ""}}
	if _, err := l2a.ValidateCreate(context.Background(), l2a); err == nil {
		t.Fatal("expected error for empty networkRef, got nil")
	}
}

func TestLayer2AttachmentValidateCreate_MTUTooLow(t *testing.T) {
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{NetworkRef: "net-1", MTU: int32Ptr(999)},
	}
	if _, err := l2a.ValidateCreate(context.Background(), l2a); err == nil {
		t.Fatal("expected error for MTU < 1000, got nil")
	}
}

func TestLayer2AttachmentValidateCreate_MTUTooHigh(t *testing.T) {
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{NetworkRef: "net-1", MTU: int32Ptr(9001)},
	}
	if _, err := l2a.ValidateCreate(context.Background(), l2a); err == nil {
		t.Fatal("expected error for MTU > 9000, got nil")
	}
}

func TestLayer2AttachmentValidateCreate_MTUBoundaryLow(t *testing.T) {
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{NetworkRef: "net-1", MTU: int32Ptr(1000)},
	}
	if _, err := l2a.ValidateCreate(context.Background(), l2a); err != nil {
		t.Fatalf("unexpected error for MTU=1000: %v", err)
	}
}

func TestLayer2AttachmentValidateCreate_InterfaceNameTooLong(t *testing.T) {
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef:    "net-1",
			InterfaceName: strPtr("1234567890123456"), // 16 chars
		},
	}
	if _, err := l2a.ValidateCreate(context.Background(), l2a); err == nil {
		t.Fatal("expected error for interfaceName > 15 chars, got nil")
	}
}

func TestLayer2AttachmentValidateCreate_InterfaceNameExact15(t *testing.T) {
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef:    "net-1",
			InterfaceName: strPtr("123456789012345"), // exactly 15 chars
		},
	}
	if _, err := l2a.ValidateCreate(context.Background(), l2a); err != nil {
		t.Fatalf("unexpected error for interfaceName of 15 chars: %v", err)
	}
}

func TestLayer2AttachmentValidateUpdate_InterfaceNameImmutable(t *testing.T) {
	old := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef:    "net-1",
			InterfaceName: strPtr("eth0"),
		},
	}
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef:    "net-1",
			InterfaceName: strPtr("eth1"),
		},
	}
	if _, err := l2a.ValidateUpdate(context.Background(), old, l2a); err == nil {
		t.Fatal("expected error for changed interfaceName, got nil")
	}
}

func TestLayer2AttachmentValidateUpdate_InterfaceNameRemovedAfterSet(t *testing.T) {
	old := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef:    "net-1",
			InterfaceName: strPtr("eth0"),
		},
	}
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{NetworkRef: "net-1"},
	}
	if _, err := l2a.ValidateUpdate(context.Background(), old, l2a); err == nil {
		t.Fatal("expected error for removing interfaceName, got nil")
	}
}

func TestLayer2AttachmentValidateUpdate_InterfaceNameUnchanged(t *testing.T) {
	old := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef:    "net-1",
			InterfaceName: strPtr("eth0"),
		},
	}
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef:    "net-1",
			InterfaceName: strPtr("eth0"),
		},
	}
	if _, err := l2a.ValidateUpdate(context.Background(), old, l2a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLayer2AttachmentValidateUpdate_InterfaceNameSetOnNew(t *testing.T) {
	old := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{NetworkRef: "net-1"},
	}
	l2a := &Layer2Attachment{
		Spec: Layer2AttachmentSpec{
			NetworkRef:    "net-1",
			InterfaceName: strPtr("eth0"),
		},
	}
	if _, err := l2a.ValidateUpdate(context.Background(), old, l2a); err != nil {
		t.Fatalf("unexpected error when setting interfaceName for the first time: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Inbound – valid cases
// ---------------------------------------------------------------------------

func TestInboundValidateCreate_WithAddresses(t *testing.T) {
	ib := &Inbound{
		Spec: InboundSpec{
			NetworkRef: "net-1",
			Addresses: &AddressAllocation{
				IPv4: []string{"10.0.0.0/24"},
			},
			Advertisement: AdvertisementConfig{Type: "bgp"},
		},
	}
	if _, err := ib.ValidateCreate(context.Background(), ib); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInboundValidateCreate_WithCount(t *testing.T) {
	ib := &Inbound{
		Spec: InboundSpec{
			NetworkRef:    "net-1",
			Count:         int32Ptr(5),
			Advertisement: AdvertisementConfig{Type: "bgp"},
		},
	}
	if _, err := ib.ValidateCreate(context.Background(), ib); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInboundValidateCreate_DualStackAddresses(t *testing.T) {
	ib := &Inbound{
		Spec: InboundSpec{
			NetworkRef: "net-1",
			Addresses: &AddressAllocation{
				IPv4: []string{"10.0.0.0/24"},
				IPv6: []string{"2001:db8::/32"},
			},
			Advertisement: AdvertisementConfig{Type: "l2"},
		},
	}
	if _, err := ib.ValidateCreate(context.Background(), ib); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInboundValidateUpdate_Valid(t *testing.T) {
	old := &Inbound{Spec: InboundSpec{
		NetworkRef:    "net-1",
		Count:         int32Ptr(2),
		Advertisement: AdvertisementConfig{Type: "bgp"},
	}}
	ib := &Inbound{Spec: InboundSpec{
		NetworkRef:    "net-1",
		Count:         int32Ptr(3),
		Advertisement: AdvertisementConfig{Type: "bgp"},
	}}
	if _, err := ib.ValidateUpdate(context.Background(), old, ib); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInboundValidateDelete_AlwaysSucceeds(t *testing.T) {
	ib := &Inbound{}
	if _, err := ib.ValidateDelete(context.Background(), ib); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Inbound – invalid cases
// ---------------------------------------------------------------------------

func TestInboundValidateCreate_EmptyNetworkRef(t *testing.T) {
	ib := &Inbound{Spec: InboundSpec{
		NetworkRef:    "",
		Count:         int32Ptr(1),
		Advertisement: AdvertisementConfig{Type: "bgp"},
	}}
	if _, err := ib.ValidateCreate(context.Background(), ib); err == nil {
		t.Fatal("expected error for empty networkRef, got nil")
	}
}

func TestInboundValidateCreate_BothCountAndAddresses(t *testing.T) {
	ib := &Inbound{Spec: InboundSpec{
		NetworkRef: "net-1",
		Count:      int32Ptr(2),
		Addresses: &AddressAllocation{
			IPv4: []string{"10.0.0.0/24"},
		},
		Advertisement: AdvertisementConfig{Type: "bgp"},
	}}
	if _, err := ib.ValidateCreate(context.Background(), ib); err == nil {
		t.Fatal("expected error for both count and addresses, got nil")
	}
}

func TestInboundValidateCreate_InvalidIPv4CIDR(t *testing.T) {
	ib := &Inbound{Spec: InboundSpec{
		NetworkRef: "net-1",
		Addresses: &AddressAllocation{
			IPv4: []string{"not-a-cidr"},
		},
		Advertisement: AdvertisementConfig{Type: "bgp"},
	}}
	if _, err := ib.ValidateCreate(context.Background(), ib); err == nil {
		t.Fatal("expected error for invalid IPv4 CIDR, got nil")
	}
}

func TestInboundValidateCreate_InvalidIPv6CIDR(t *testing.T) {
	ib := &Inbound{Spec: InboundSpec{
		NetworkRef: "net-1",
		Addresses: &AddressAllocation{
			IPv6: []string{"xyz"},
		},
		Advertisement: AdvertisementConfig{Type: "bgp"},
	}}
	if _, err := ib.ValidateCreate(context.Background(), ib); err == nil {
		t.Fatal("expected error for invalid IPv6 CIDR, got nil")
	}
}

// ---------------------------------------------------------------------------
// Outbound – valid cases
// ---------------------------------------------------------------------------

func TestOutboundValidateCreate_WithAddresses(t *testing.T) {
	ob := &Outbound{
		Spec: OutboundSpec{
			NetworkRef: "net-1",
			Addresses: &AddressAllocation{
				IPv4: []string{"10.0.0.0/24"},
			},
		},
	}
	if _, err := ob.ValidateCreate(context.Background(), ob); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOutboundValidateCreate_WithCount(t *testing.T) {
	ob := &Outbound{
		Spec: OutboundSpec{
			NetworkRef: "net-1",
			Count:      int32Ptr(3),
		},
	}
	if _, err := ob.ValidateCreate(context.Background(), ob); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOutboundValidateCreate_DualStackAddresses(t *testing.T) {
	ob := &Outbound{
		Spec: OutboundSpec{
			NetworkRef: "net-1",
			Addresses: &AddressAllocation{
				IPv4: []string{"10.0.0.0/24"},
				IPv6: []string{"2001:db8::/32"},
			},
		},
	}
	if _, err := ob.ValidateCreate(context.Background(), ob); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOutboundValidateUpdate_Valid(t *testing.T) {
	old := &Outbound{Spec: OutboundSpec{
		NetworkRef: "net-1",
		Count:      int32Ptr(2),
	}}
	ob := &Outbound{Spec: OutboundSpec{
		NetworkRef: "net-1",
		Count:      int32Ptr(4),
	}}
	if _, err := ob.ValidateUpdate(context.Background(), old, ob); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOutboundValidateDelete_AlwaysSucceeds(t *testing.T) {
	ob := &Outbound{}
	if _, err := ob.ValidateDelete(context.Background(), ob); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Outbound – invalid cases
// ---------------------------------------------------------------------------

func TestOutboundValidateCreate_EmptyNetworkRef(t *testing.T) {
	ob := &Outbound{Spec: OutboundSpec{NetworkRef: "", Count: int32Ptr(1)}}
	if _, err := ob.ValidateCreate(context.Background(), ob); err == nil {
		t.Fatal("expected error for empty networkRef, got nil")
	}
}

func TestOutboundValidateCreate_BothCountAndAddresses(t *testing.T) {
	ob := &Outbound{Spec: OutboundSpec{
		NetworkRef: "net-1",
		Count:      int32Ptr(2),
		Addresses: &AddressAllocation{
			IPv4: []string{"10.0.0.0/24"},
		},
	}}
	if _, err := ob.ValidateCreate(context.Background(), ob); err == nil {
		t.Fatal("expected error for both count and addresses, got nil")
	}
}

func TestOutboundValidateCreate_InvalidIPv4CIDR(t *testing.T) {
	ob := &Outbound{Spec: OutboundSpec{
		NetworkRef: "net-1",
		Addresses: &AddressAllocation{
			IPv4: []string{"bad-cidr"},
		},
	}}
	if _, err := ob.ValidateCreate(context.Background(), ob); err == nil {
		t.Fatal("expected error for invalid IPv4 CIDR, got nil")
	}
}

func TestOutboundValidateCreate_InvalidIPv6CIDR(t *testing.T) {
	ob := &Outbound{Spec: OutboundSpec{
		NetworkRef: "net-1",
		Addresses: &AddressAllocation{
			IPv6: []string{"not-valid"},
		},
	}}
	if _, err := ob.ValidateCreate(context.Background(), ob); err == nil {
		t.Fatal("expected error for invalid IPv6 CIDR, got nil")
	}
}
