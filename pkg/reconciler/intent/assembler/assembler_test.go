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

package assembler

import (
	"testing"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/builder"
)

func TestAssemble_Nil(t *testing.T) {
	spec, err := Assemble(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Layer2s) != 0 {
		t.Errorf("expected empty Layer2s, got %d", len(spec.Layer2s))
	}
	if len(spec.FabricVRFs) != 0 {
		t.Errorf("expected empty FabricVRFs, got %d", len(spec.FabricVRFs))
	}
}

func TestAssemble_SingleContribution(t *testing.T) {
	c := builder.NewNodeContribution()
	c.Layer2s["100"] = networkv1alpha1.Layer2{
		VNI:  10100,
		VLAN: 100,
		MTU:  9000,
	}
	c.FabricVRFs["tenant-a"] = networkv1alpha1.FabricVRF{
		VNI: 1001,
		VRF: networkv1alpha1.VRF{
			BGPPeers: []networkv1alpha1.BGPPeer{
				{RemoteASN: 65001},
			},
		},
	}

	spec, err := Assemble([]*builder.NodeContribution{c})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(spec.Layer2s) != 1 {
		t.Fatalf("expected 1 Layer2, got %d", len(spec.Layer2s))
	}
	l2 := spec.Layer2s["100"]
	if l2.VNI != 10100 {
		t.Errorf("expected VNI 10100, got %d", l2.VNI)
	}

	if len(spec.FabricVRFs) != 1 {
		t.Fatalf("expected 1 FabricVRF, got %d", len(spec.FabricVRFs))
	}
	fvrf := spec.FabricVRFs["tenant-a"]
	if len(fvrf.BGPPeers) != 1 {
		t.Errorf("expected 1 BGPPeer, got %d", len(fvrf.BGPPeers))
	}
}

func TestAssemble_MergeFabricVRFs(t *testing.T) {
	c1 := builder.NewNodeContribution()
	c1.FabricVRFs["vrf-a"] = networkv1alpha1.FabricVRF{
		VNI: 1001,
		VRF: networkv1alpha1.VRF{
			BGPPeers: []networkv1alpha1.BGPPeer{
				{RemoteASN: 65001},
			},
			StaticRoutes: []networkv1alpha1.StaticRoute{
				{Prefix: "10.0.0.0/8"},
			},
		},
		EVPNImportRouteTargets: []string{"65000:1001"},
		EVPNExportRouteTargets: []string{"65000:1001"},
	}

	c2 := builder.NewNodeContribution()
	c2.FabricVRFs["vrf-a"] = networkv1alpha1.FabricVRF{
		VRF: networkv1alpha1.VRF{
			BGPPeers: []networkv1alpha1.BGPPeer{
				{RemoteASN: 65002},
			},
			StaticRoutes: []networkv1alpha1.StaticRoute{
				{Prefix: "172.16.0.0/12"},
			},
		},
	}

	spec, err := Assemble([]*builder.NodeContribution{c1, c2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fvrf := spec.FabricVRFs["vrf-a"]
	if fvrf.VNI != 1001 {
		t.Errorf("expected VNI 1001 (from first contribution), got %d", fvrf.VNI)
	}
	if len(fvrf.BGPPeers) != 2 {
		t.Errorf("expected 2 merged BGPPeers, got %d", len(fvrf.BGPPeers))
	}
	if len(fvrf.StaticRoutes) != 2 {
		t.Errorf("expected 2 merged StaticRoutes, got %d", len(fvrf.StaticRoutes))
	}
}

func TestAssemble_MergeClusterVRF(t *testing.T) {
	c1 := builder.NewNodeContribution()
	c1.ClusterVRF = &networkv1alpha1.VRF{
		BGPPeers: []networkv1alpha1.BGPPeer{
			{RemoteASN: 65001},
		},
	}

	c2 := builder.NewNodeContribution()
	c2.ClusterVRF = &networkv1alpha1.VRF{
		BGPPeers: []networkv1alpha1.BGPPeer{
			{RemoteASN: 65002},
		},
		StaticRoutes: []networkv1alpha1.StaticRoute{
			{Prefix: "10.0.0.0/8"},
		},
	}

	spec, err := Assemble([]*builder.NodeContribution{c1, c2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.ClusterVRF == nil {
		t.Fatal("expected ClusterVRF to be set")
	}
	if len(spec.ClusterVRF.BGPPeers) != 2 {
		t.Errorf("expected 2 merged BGPPeers, got %d", len(spec.ClusterVRF.BGPPeers))
	}
	if len(spec.ClusterVRF.StaticRoutes) != 1 {
		t.Errorf("expected 1 StaticRoute, got %d", len(spec.ClusterVRF.StaticRoutes))
	}
}

func TestAssemble_SkipsNilContributions(t *testing.T) {
	c := builder.NewNodeContribution()
	c.Layer2s["100"] = networkv1alpha1.Layer2{VNI: 100, VLAN: 100, MTU: 9000}

	spec, err := Assemble([]*builder.NodeContribution{nil, c, nil})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Layer2s) != 1 {
		t.Errorf("expected 1 Layer2, got %d", len(spec.Layer2s))
	}
}

func TestAssemble_MergeLoopbacks(t *testing.T) {
	c1 := builder.NewNodeContribution()
	c1.FabricVRFs["vrf-a"] = networkv1alpha1.FabricVRF{
		VNI: 1001,
		VRF: networkv1alpha1.VRF{
			Loopbacks: map[string]networkv1alpha1.Loopback{
				"lo0": {IPAddresses: []string{"10.0.0.1/32"}},
			},
		},
	}

	c2 := builder.NewNodeContribution()
	c2.FabricVRFs["vrf-a"] = networkv1alpha1.FabricVRF{
		VRF: networkv1alpha1.VRF{
			Loopbacks: map[string]networkv1alpha1.Loopback{
				"lo1": {IPAddresses: []string{"10.0.0.2/32"}},
			},
		},
	}

	spec, err := Assemble([]*builder.NodeContribution{c1, c2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fvrf := spec.FabricVRFs["vrf-a"]
	if len(fvrf.Loopbacks) != 2 {
		t.Errorf("expected 2 merged loopbacks, got %d", len(fvrf.Loopbacks))
	}
	if _, ok := fvrf.Loopbacks["lo0"]; !ok {
		t.Error("expected lo0 loopback")
	}
	if _, ok := fvrf.Loopbacks["lo1"]; !ok {
		t.Error("expected lo1 loopback")
	}
}

func TestAssemble_MergeLocalVRFs(t *testing.T) {
	c1 := builder.NewNodeContribution()
	c1.LocalVRFs["sbr-1"] = networkv1alpha1.VRF{
		StaticRoutes: []networkv1alpha1.StaticRoute{{Prefix: "10.0.0.0/8"}},
	}

	c2 := builder.NewNodeContribution()
	c2.LocalVRFs["sbr-1"] = networkv1alpha1.VRF{
		StaticRoutes: []networkv1alpha1.StaticRoute{{Prefix: "172.16.0.0/12"}},
	}

	spec, err := Assemble([]*builder.NodeContribution{c1, c2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	local := spec.LocalVRFs["sbr-1"]
	if len(local.StaticRoutes) != 2 {
		t.Errorf("expected 2 merged static routes, got %d", len(local.StaticRoutes))
	}
}
