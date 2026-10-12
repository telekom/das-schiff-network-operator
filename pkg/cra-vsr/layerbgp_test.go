/*
Copyright 2026.

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

package cra

import (
	"encoding/xml"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/types"
)

func TestVRFImportRouteMaps(t *testing.T) {
	cluster := manager.baseConfig.ClusterVRF.Name
	mgmt := manager.baseConfig.ManagementVRF.Name
	for _, kind := range []string{"local", "fabric"} {
		for _, tc := range []struct {
			name    string
			imports []v1alpha1.VRFImport
		}{
			{name: "nil imports"},
			{name: "empty imports", imports: []v1alpha1.VRFImport{}},
			{
				name: "explicit imports",
				imports: []v1alpha1.VRFImport{
					{FromVRF: cluster, Filter: v1alpha1.Filter{
						DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
					}},
					{FromVRF: mgmt, Filter: v1alpha1.Filter{
						DefaultAction: v1alpha1.Action{Type: v1alpha1.Accept},
					}},
				},
			},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				conf := v1alpha1.VRF{VRFImports: tc.imports}
				spec := &v1alpha1.NodeNetworkConfigSpec{}
				if kind == "local" {
					spec.LocalVRFs = map[string]v1alpha1.VRF{"test": conf}
				} else {
					spec.FabricVRFs = map[string]v1alpha1.FabricVRF{
						"test": {VRF: conf, VNI: 2001000},
					}
				}
				mgr := *manager
				mgr.running = mgr.startup
				generated, err := mgr.makeVRouter(spec)
				require.NoError(t, err)
				assertVRFImportRouteMaps(t, generated, mgr.WorkNSName, tc.imports)
			})
		}
	}
}

func assertVRFImportRouteMaps(t *testing.T, generated *VRouter, nsName string, imports []v1alpha1.VRFImport) {
	t.Helper()
	ns := findNamespace(generated, nsName)
	require.NotNil(t, ns)
	vrf := findVRFByName(ns, "test")
	require.NotNil(t, vrf)
	var sources []string
	for _, imprt := range imports {
		sources = append(sources, imprt.FromVRF)
	}
	for _, af := range []*BGPUcast{vrf.Routing.BGP.AF.UcastV4, vrf.Routing.BGP.AF.UcastV6} {
		if len(imports) == 0 {
			require.Nil(t, af.VRFImports)
			continue
		}
		require.NotNil(t, af.VRFImports)
		require.Equal(t, sources, af.VRFImports.Imports.VRFs)
		require.Equal(t, []string{"rm_test_import"}, af.VRFImports.Imports.RouteMaps)
	}

	maps := map[string]RouteMap{}
	for _, rtmap := range generated.Routing.RouteMaps {
		maps[rtmap.Name] = rtmap
	}
	if len(imports) == 0 {
		require.NotContains(t, maps, "rm_test_import")
	} else {
		require.Len(t, maps["rm_test_import"].Seqs, len(imports))
		for i, imprt := range imports {
			seq := maps["rm_test_import"].Seqs[i]
			require.Equal(t, imprt.FromVRF, *seq.Match.SourceVRF)
			require.Equal(t, "rm_test_import_"+imprt.FromVRF, *seq.Call)
			require.Equal(t, []RtMapSeq{{Num: 10, Policy: (LayerBGP{}).convActionToPolicy(imprt.Filter.DefaultAction)}},
				maps[*seq.Call].Seqs)
		}
	}

	data, err := xml.Marshal(VRouterConfig{VRouter: *generated})
	require.NoError(t, err)
	if len(imports) == 0 {
		require.NotContains(t, string(data), "rm_test_import")
	} else {
		require.Contains(t, string(data), "<route-map>rm_test_import</route-map>")
	}
}

func TestMultiVRFLayer2StaticRoutes(t *testing.T) {
	spec := &v1alpha1.NodeNetworkConfigSpec{
		LocalVRFs:  map[string]v1alpha1.VRF{},
		FabricVRFs: map[string]v1alpha1.FabricVRF{},
		Layer2s:    map[string]v1alpha1.Layer2{},
	}
	for _, fixture := range []struct {
		name string
		vlan uint16
		vni  uint32
	}{
		{name: "l-1111111111111", vlan: 1000, vni: 3501000},
		{name: "l-2222222222222", vlan: 1001, vni: 3501001},
	} {
		name := fixture.name
		spec.LocalVRFs[name] = v1alpha1.VRF{
			StaticRoutes: []v1alpha1.StaticRoute{
				{Prefix: "192.0.2.0/24", NextHop: &v1alpha1.NextHop{Vrf: types.ToPtr("fabric-a")}},
				{Prefix: "2001:db8:2::/64", NextHop: &v1alpha1.NextHop{Vrf: types.ToPtr("fabric-b")}},
			},
		}
		spec.Layer2s[name] = v1alpha1.Layer2{
			VLAN: fixture.vlan, VNI: fixture.vni, MTU: 1500,
			IRB: &v1alpha1.IRB{
				VRF: name, MACAddress: "00:00:5e:00:01:01",
				IPAddresses: []string{"198.51.100.1/24", "2001:db8:1::1/64"},
			},
		}
	}
	for _, fixture := range []struct {
		name string
		vni  uint32
	}{
		{name: "fabric-a", vni: 2001000},
		{name: "fabric-b", vni: 2001001},
	} {
		spec.FabricVRFs[fixture.name] = v1alpha1.FabricVRF{
			VNI: fixture.vni,
			VRF: v1alpha1.VRF{
				StaticRoutes: []v1alpha1.StaticRoute{
					{Prefix: "198.51.100.0/24", NextHop: &v1alpha1.NextHop{Vrf: types.ToPtr("l-1111111111111")}},
					{Prefix: "2001:db8:1::/64", NextHop: &v1alpha1.NextHop{Vrf: types.ToPtr("l-2222222222222")}},
				},
				VRFImports: []v1alpha1.VRFImport{
					{FromVRF: manager.baseConfig.ClusterVRF.Name, Filter: v1alpha1.Filter{
						DefaultAction: v1alpha1.Action{Type: v1alpha1.Reject},
					}},
				},
			},
		}
	}

	mgr := *manager
	mgr.running = mgr.startup
	generated, err := mgr.makeVRouter(spec)
	require.NoError(t, err)
	data, err := xml.Marshal(VRouterConfig{VRouter: *generated})
	require.NoError(t, err)
	ns := findNamespace(generated, mgr.WorkNSName)
	require.NotNil(t, ns)
	for name := range spec.LocalVRFs {
		require.NotContains(t, string(data), "rm_"+name+"_import")
		vrf := findVRFByName(ns, name)
		require.NotNil(t, vrf)
		require.Len(t, vrf.Interfaces.Bridges, 1)
		require.Nil(t, vrf.Routing.BGP.AF.UcastV4.VRFImports)
		require.Nil(t, vrf.Routing.BGP.AF.UcastV6.VRFImports)
		require.Equal(t, "fabric-a", *vrf.Routing.Static.IPv4[0].NextHops[0].VRF)
		require.Equal(t, "fabric-b", *vrf.Routing.Static.IPv6[0].NextHops[0].VRF)
	}
	for name := range spec.FabricVRFs {
		vrf := findVRFByName(ns, name)
		require.NotNil(t, vrf)
		require.Equal(t, "l-1111111111111", *vrf.Routing.Static.IPv4[0].NextHops[0].VRF)
		require.Equal(t, "l-2222222222222", *vrf.Routing.Static.IPv6[0].NextHops[0].VRF)
		require.Contains(t, string(data), "<route-map>rm_"+name+"_import</route-map>")
	}
}
