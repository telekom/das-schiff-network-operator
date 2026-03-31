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

package resolver

import (
	"testing"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptr[T any](v T) *T { return &v }

func TestResolveVRFs(t *testing.T) {
	vrfs := []nc.VRF{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"},
			Spec:       nc.VRFSpec{VRF: "vrf-a", VNI: ptr(int32(1001)), RouteTarget: ptr("65000:1001")},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "tenant-b"},
			Spec:       nc.VRFSpec{VRF: "vrf-b", VNI: ptr(int32(1002))},
		},
	}

	resolved := ResolveVRFs(vrfs)

	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolved VRFs, got %d", len(resolved))
	}

	a, ok := resolved["tenant-a"]
	if !ok {
		t.Fatal("expected tenant-a in resolved map")
	}
	if a.Spec.VRF != "vrf-a" {
		t.Errorf("expected VRF name vrf-a, got %s", a.Spec.VRF)
	}
	if *a.Spec.VNI != 1001 {
		t.Errorf("expected VNI 1001, got %d", *a.Spec.VNI)
	}
	if *a.Spec.RouteTarget != "65000:1001" {
		t.Errorf("expected RT 65000:1001, got %s", *a.Spec.RouteTarget)
	}

	b := resolved["tenant-b"]
	if b.Spec.RouteTarget != nil {
		t.Errorf("expected nil RouteTarget for tenant-b, got %v", b.Spec.RouteTarget)
	}
}

func TestResolveVRFs_Empty(t *testing.T) {
	resolved := ResolveVRFs(nil)
	if len(resolved) != 0 {
		t.Fatalf("expected 0 resolved VRFs, got %d", len(resolved))
	}
}

func TestResolveNetworks(t *testing.T) {
	networks := []nc.Network{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "mgmt-net"},
			Spec: nc.NetworkSpec{
				IPv4: &nc.IPNetwork{CIDR: "10.0.0.0/24"},
				VLAN: ptr(int32(100)),
				VNI:  ptr(int32(10100)),
			},
		},
	}

	resolved := ResolveNetworks(networks)

	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved Network, got %d", len(resolved))
	}

	net := resolved["mgmt-net"]
	if net.Spec.IPv4.CIDR != "10.0.0.0/24" {
		t.Errorf("expected CIDR 10.0.0.0/24, got %s", net.Spec.IPv4.CIDR)
	}
	if *net.Spec.VLAN != 100 {
		t.Errorf("expected VLAN 100, got %d", *net.Spec.VLAN)
	}
}

func TestResolveDestinations_WithVRF(t *testing.T) {
	vrfs := map[string]*ResolvedVRF{
		"tenant-a": {
			Name: "tenant-a",
			Spec: nc.VRFSpec{VRF: "vrf-a", VNI: ptr(int32(1001))},
		},
	}

	destinations := []nc.Destination{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "corp-dc"},
			Spec: nc.DestinationSpec{
				VRFRef:   ptr("tenant-a"),
				Prefixes: []string{"10.0.0.0/8"},
			},
		},
	}

	resolved, err := ResolveDestinations(destinations, vrfs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d := resolved["corp-dc"]
	if d.VRFSpec == nil {
		t.Fatal("expected VRFSpec to be set")
	}
	if d.VRFSpec.VRF != "vrf-a" {
		t.Errorf("expected VRF vrf-a, got %s", d.VRFSpec.VRF)
	}
}

func TestResolveDestinations_NextHopOnly(t *testing.T) {
	vrfs := map[string]*ResolvedVRF{}

	destinations := []nc.Destination{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "external"},
			Spec: nc.DestinationSpec{
				NextHop: &nc.NextHopConfig{},
			},
		},
	}

	resolved, err := ResolveDestinations(destinations, vrfs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d := resolved["external"]
	if d.VRFSpec != nil {
		t.Error("expected nil VRFSpec for nextHop-only destination")
	}
}

func TestResolveDestinations_UnknownVRF(t *testing.T) {
	vrfs := map[string]*ResolvedVRF{}

	destinations := []nc.Destination{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-dest"},
			Spec: nc.DestinationSpec{
				VRFRef: ptr("nonexistent"),
			},
		},
	}

	_, err := ResolveDestinations(destinations, vrfs)
	if err == nil {
		t.Fatal("expected error for unknown VRF reference")
	}
}

func TestResolveAll(t *testing.T) {
	fetched := &FetchedResources{
		VRFs: []nc.VRF{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "vrf-1"},
				Spec:       nc.VRFSpec{VRF: "vrf-1", VNI: ptr(int32(100))},
			},
		},
		Networks: []nc.Network{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "net-1"},
				Spec:       nc.NetworkSpec{VLAN: ptr(int32(10))},
			},
		},
		Destinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "dst-1"},
				Spec:       nc.DestinationSpec{VRFRef: ptr("vrf-1")},
			},
		},
	}

	resolved, err := ResolveAll(fetched)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resolved.VRFs) != 1 {
		t.Errorf("expected 1 VRF, got %d", len(resolved.VRFs))
	}
	if len(resolved.Networks) != 1 {
		t.Errorf("expected 1 Network, got %d", len(resolved.Networks))
	}
	if len(resolved.Destinations) != 1 {
		t.Errorf("expected 1 Destination, got %d", len(resolved.Destinations))
	}
	if resolved.Destinations["dst-1"].VRFSpec == nil {
		t.Error("expected destination to have resolved VRF")
	}
}

func TestResolveAll_DestinationError(t *testing.T) {
	fetched := &FetchedResources{
		Destinations: []nc.Destination{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bad"},
				Spec:       nc.DestinationSpec{VRFRef: ptr("missing-vrf")},
			},
		},
	}

	_, err := ResolveAll(fetched)
	if err == nil {
		t.Fatal("expected error from ResolveAll with missing VRF")
	}
}
