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
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func int32Ptr(v int32) *int32 { return &v }
func strPtr(v string) *string { return &v }

// ---------------------------------------------------------------------------
// Network – valid cases
// ---------------------------------------------------------------------------

func TestNetworkValidateCreate_IPv4Only(t *testing.T) {
	n := &Network{Spec: NetworkSpec{IPv4: &IPNetwork{CIDR: "10.0.0.0/24"}}}
	if _, err := n.ValidateCreate(context.Background(), n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNetworkValidateCreate_IPv6Only(t *testing.T) {
	n := &Network{Spec: NetworkSpec{IPv6: &IPNetwork{CIDR: "2001:db8::/32"}}}
	if _, err := n.ValidateCreate(context.Background(), n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNetworkValidateCreate_DualStack(t *testing.T) {
	n := &Network{Spec: NetworkSpec{
		IPv4: &IPNetwork{CIDR: "10.0.0.0/24"},
		IPv6: &IPNetwork{CIDR: "2001:db8::/32"},
	}}
	if _, err := n.ValidateCreate(context.Background(), n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNetworkValidateCreate_WithVLAN(t *testing.T) {
	n := &Network{Spec: NetworkSpec{
		IPv4: &IPNetwork{CIDR: "10.0.0.0/24"},
		VLAN: int32Ptr(100),
	}}
	if _, err := n.ValidateCreate(context.Background(), n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNetworkValidateCreate_WithVNI(t *testing.T) {
	n := &Network{Spec: NetworkSpec{
		IPv4: &IPNetwork{CIDR: "10.0.0.0/24"},
		VNI:  int32Ptr(5000),
	}}
	if _, err := n.ValidateCreate(context.Background(), n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNetworkValidateUpdate_Valid(t *testing.T) {
	old := &Network{Spec: NetworkSpec{IPv4: &IPNetwork{CIDR: "10.0.0.0/24"}}}
	n := &Network{Spec: NetworkSpec{IPv4: &IPNetwork{CIDR: "10.0.1.0/24"}}}
	if _, err := n.ValidateUpdate(context.Background(), old, n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNetworkValidateDelete_AlwaysSucceeds(t *testing.T) {
	n := &Network{}
	if _, err := n.ValidateDelete(context.Background(), n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Network – invalid cases
// ---------------------------------------------------------------------------

func TestNetworkValidateCreate_NoIPFamily(t *testing.T) {
	n := &Network{Spec: NetworkSpec{}}
	if _, err := n.ValidateCreate(context.Background(), n); err == nil {
		t.Fatal("expected error for missing IPv4 and IPv6, got nil")
	}
}

func TestNetworkValidateCreate_InvalidIPv4CIDR(t *testing.T) {
	n := &Network{Spec: NetworkSpec{IPv4: &IPNetwork{CIDR: "not-a-cidr"}}}
	if _, err := n.ValidateCreate(context.Background(), n); err == nil {
		t.Fatal("expected error for invalid IPv4 CIDR, got nil")
	}
}

func TestNetworkValidateCreate_InvalidIPv6CIDR(t *testing.T) {
	n := &Network{Spec: NetworkSpec{IPv6: &IPNetwork{CIDR: "xyz"}}}
	if _, err := n.ValidateCreate(context.Background(), n); err == nil {
		t.Fatal("expected error for invalid IPv6 CIDR, got nil")
	}
}

// A Network CIDR must be the network address (host bits zero); an authored host
// address like "10.0.0.1/24" is rejected so the anycast gateway (network+1) is
// unambiguous.
func TestNetworkValidateCreate_IPv4NotNetworkAddress(t *testing.T) {
	n := &Network{Spec: NetworkSpec{IPv4: &IPNetwork{CIDR: "10.0.0.1/24"}}}
	if _, err := n.ValidateCreate(context.Background(), n); err == nil {
		t.Fatal("expected error for non-network-address IPv4 CIDR, got nil")
	}
}

func TestNetworkValidateCreate_IPv6NotNetworkAddress(t *testing.T) {
	n := &Network{Spec: NetworkSpec{IPv6: &IPNetwork{CIDR: "2001:db8::1/64"}}}
	if _, err := n.ValidateCreate(context.Background(), n); err == nil {
		t.Fatal("expected error for non-network-address IPv6 CIDR, got nil")
	}
}

// Point-to-point and single-host prefixes are valid as long as they are in
// canonical (masked) form.
func TestNetworkValidateCreate_NetworkAddressEdgePrefixes(t *testing.T) {
	for _, cidr := range []string{"10.0.0.0/31", "10.0.0.4/32", "2001:db8::/127"} {
		n := &Network{Spec: NetworkSpec{IPv4: &IPNetwork{CIDR: cidr}}}
		if strings.Contains(cidr, ":") {
			n = &Network{Spec: NetworkSpec{IPv6: &IPNetwork{CIDR: cidr}}}
		}
		if _, err := n.ValidateCreate(context.Background(), n); err != nil {
			t.Fatalf("unexpected error for canonical CIDR %q: %v", cidr, err)
		}
	}
}

func TestNetworkValidateCreate_VNIZero(t *testing.T) {
	n := &Network{Spec: NetworkSpec{
		IPv4: &IPNetwork{CIDR: "10.0.0.0/24"},
		VNI:  int32Ptr(0),
	}}
	if _, err := n.ValidateCreate(context.Background(), n); err == nil {
		t.Fatal("expected error for VNI=0, got nil")
	}
}

func TestNetworkValidateCreate_VNINegative(t *testing.T) {
	n := &Network{Spec: NetworkSpec{
		IPv4: &IPNetwork{CIDR: "10.0.0.0/24"},
		VNI:  int32Ptr(-1),
	}}
	if _, err := n.ValidateCreate(context.Background(), n); err == nil {
		t.Fatal("expected error for negative VNI, got nil")
	}
}

func TestNetworkValidateCreate_VLANZero(t *testing.T) {
	n := &Network{Spec: NetworkSpec{
		IPv4: &IPNetwork{CIDR: "10.0.0.0/24"},
		VLAN: int32Ptr(0),
	}}
	if _, err := n.ValidateCreate(context.Background(), n); err == nil {
		t.Fatal("expected error for VLAN=0, got nil")
	}
}

func TestNetworkValidateCreate_VLANTooHigh(t *testing.T) {
	n := &Network{Spec: NetworkSpec{
		IPv4: &IPNetwork{CIDR: "10.0.0.0/24"},
		VLAN: int32Ptr(4095),
	}}
	if _, err := n.ValidateCreate(context.Background(), n); err == nil {
		t.Fatal("expected error for VLAN=4095, got nil")
	}
}

// ---------------------------------------------------------------------------
// VRF – valid cases
// ---------------------------------------------------------------------------

func TestVRFValidateCreate_Minimal(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: "prod"}}
	if _, err := v.ValidateCreate(context.Background(), v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVRFValidateCreate_WithVNI(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: "prod", VNI: int32Ptr(5000)}}
	if _, err := v.ValidateCreate(context.Background(), v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVRFValidateCreate_WithRouteTarget(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: "prod", RouteTarget: strPtr("65000:100")}}
	if _, err := v.ValidateCreate(context.Background(), v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVRFValidateCreate_Full(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: "prod", VNI: int32Ptr(10000), RouteTarget: strPtr("65000:200")}}
	if _, err := v.ValidateCreate(context.Background(), v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVRFValidateUpdate_Valid(t *testing.T) {
	old := &VRF{Spec: VRFSpec{VRF: "prod"}}
	v := &VRF{Spec: VRFSpec{VRF: "prod", VNI: int32Ptr(100)}}
	if _, err := v.ValidateUpdate(context.Background(), old, v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVRFValidateDelete_AlwaysSucceeds(t *testing.T) {
	v := &VRF{}
	if _, err := v.ValidateDelete(context.Background(), v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VRF – invalid cases
// ---------------------------------------------------------------------------

func TestVRFValidateCreate_EmptyVRFName(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: ""}}
	if _, err := v.ValidateCreate(context.Background(), v); err == nil {
		t.Fatal("expected error for empty VRF name, got nil")
	}
}

func TestVRFValidateCreate_ReducibleLongName(t *testing.T) {
	// A readable name longer than 15 chars that still reduces to fit is accepted.
	v := &VRF{Spec: VRFSpec{VRF: "payment_gateway_worker"}}
	if _, err := v.ValidateCreate(context.Background(), v); err != nil {
		t.Fatalf("expected reducible long name to be accepted, got %v", err)
	}
}

func TestVRFValidateCreate_IrreducibleName(t *testing.T) {
	// A long incompressible name (no removable vowels / underscores) is rejected.
	v := &VRF{Spec: VRFSpec{VRF: "wwwwwwwwxxxxxxxx"}}
	if _, err := v.ValidateCreate(context.Background(), v); err == nil {
		t.Fatal("expected error for irreducible VRF name, got nil")
	}
}

func TestVRFValidateCreate_VNIZero(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: "prod", VNI: int32Ptr(0)}}
	if _, err := v.ValidateCreate(context.Background(), v); err == nil {
		t.Fatal("expected error for VNI=0, got nil")
	}
}

func TestVRFValidateCreate_VNINegative(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: "prod", VNI: int32Ptr(-5)}}
	if _, err := v.ValidateCreate(context.Background(), v); err == nil {
		t.Fatal("expected error for negative VNI, got nil")
	}
}

func TestVRFValidateCreate_InvalidRouteTarget_NoColon(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: "prod", RouteTarget: strPtr("65000")}}
	if _, err := v.ValidateCreate(context.Background(), v); err == nil {
		t.Fatal("expected error for invalid route target, got nil")
	}
}

func TestVRFValidateCreate_InvalidRouteTarget_Letters(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: "prod", RouteTarget: strPtr("abc:def")}}
	if _, err := v.ValidateCreate(context.Background(), v); err == nil {
		t.Fatal("expected error for non-numeric route target, got nil")
	}
}

func TestVRFValidateCreate_InvalidRouteTarget_Empty(t *testing.T) {
	v := &VRF{Spec: VRFSpec{VRF: "prod", RouteTarget: strPtr("")}}
	if _, err := v.ValidateCreate(context.Background(), v); err == nil {
		t.Fatal("expected error for empty route target, got nil")
	}
}
