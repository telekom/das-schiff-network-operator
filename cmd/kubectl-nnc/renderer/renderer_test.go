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

package renderer

import (
	"bytes"
	"strings"
	"testing"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptrString(s string) *string { return &s }

func testNNC() *networkv1alpha1.NodeNetworkConfig {
	return &networkv1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-1"},
		Spec: networkv1alpha1.NodeNetworkConfigSpec{
			Revision: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			Layer2s: map[string]networkv1alpha1.Layer2{
				"prod-vlan100": {
					VNI: 100, VLAN: 100, MTU: 9000, RouteTarget: "65000:100",
					IRB: &networkv1alpha1.IRB{
						VRF:         "internet",
						MACAddress:  "00:00:5e:00:01:01",
						IPAddresses: []string{"198.51.100.1/24"},
					},
				},
			},
			FabricVRFs: map[string]networkv1alpha1.FabricVRF{
				"internet": {
					VRF: networkv1alpha1.VRF{
						StaticRoutes: []networkv1alpha1.StaticRoute{
							{Prefix: "198.51.100.10/32"},
						},
						BGPPeers: []networkv1alpha1.BGPPeer{
							{
								Address:   ptrString("10.0.0.1"),
								RemoteASN: 65001,
								IPv4:      &networkv1alpha1.AddressFamily{},
							},
						},
					},
					VNI:                    1000,
					EVPNImportRouteTargets: []string{"65000:1000"},
					EVPNExportRouteTargets: []string{"65000:1000"},
				},
			},
			LocalVRFs: map[string]networkv1alpha1.VRF{
				"s-internet": {
					StaticRoutes: []networkv1alpha1.StaticRoute{
						{Prefix: "0.0.0.0/0", NextHop: &networkv1alpha1.NextHop{Vrf: ptrString("internet")}},
					},
					VRFImports: []networkv1alpha1.VRFImport{
						{FromVRF: "cluster", Filter: networkv1alpha1.Filter{
							DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
						}},
					},
				},
			},
			ClusterVRF: &networkv1alpha1.VRF{
				PolicyRoutes: []networkv1alpha1.PolicyRoute{
					{
						TrafficMatch: networkv1alpha1.TrafficMatch{SrcPrefix: ptrString("10.244.0.0/16")},
						NextHop:      networkv1alpha1.NextHop{Vrf: ptrString("s-internet")},
					},
				},
			},
		},
		Status: networkv1alpha1.NodeNetworkConfigStatus{
			ConfigStatus: "provisioned",
		},
	}
}

func TestRenderNNC_NoColor(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)

	nnc := testNNC()
	origins := Origins{
		"layer2s/prod-vlan100": "Layer2Attachment/my-l2a",
		"fabricVRFs/internet":  "Inbound/my-vip",
	}

	r.RenderNNC(nnc, origins)
	output := buf.String()

	// Header.
	assert.Contains(t, output, "NodeNetworkConfig: worker-1")
	assert.Contains(t, output, "Revision: a1b2c3d4e5f6a1b2")
	assert.Contains(t, output, "provisioned")

	// Layer2.
	assert.Contains(t, output, "Layer2s (1):")
	assert.Contains(t, output, "prod-vlan100 (VNI=100, VLAN=100, MTU=9000)")
	assert.Contains(t, output, "← Layer2Attachment/my-l2a")
	assert.Contains(t, output, "IRB: VRF=internet, MAC=00:00:5e:00:01:01")

	// FabricVRFs.
	assert.Contains(t, output, "FabricVRFs (1):")
	assert.Contains(t, output, "internet (VNI=1000, RT=65000:1000)")
	assert.Contains(t, output, "← Inbound/my-vip")
	assert.Contains(t, output, "10.0.0.1")
	assert.Contains(t, output, "65001")
	assert.Contains(t, output, "198.51.100.10/32")

	// LocalVRFs.
	assert.Contains(t, output, "LocalVRFs (1):")
	assert.Contains(t, output, "s-internet")
	assert.Contains(t, output, "0.0.0.0/0")
	assert.Contains(t, output, "vrf:internet")
	assert.Contains(t, output, "cluster")
	assert.Contains(t, output, "accept")

	// ClusterVRF.
	assert.Contains(t, output, "ClusterVRF:")
	assert.Contains(t, output, "10.244.0.0/16")
	assert.Contains(t, output, "s-internet")
}

func TestRenderNNC_EmptySections(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)

	nnc := &networkv1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-node"},
		Spec:       networkv1alpha1.NodeNetworkConfigSpec{Revision: "abc123"},
	}

	r.RenderNNC(nnc, nil)
	output := buf.String()

	assert.Contains(t, output, "NodeNetworkConfig: empty-node")
	assert.NotContains(t, output, "Layer2s")
	assert.NotContains(t, output, "FabricVRFs")
	assert.NotContains(t, output, "LocalVRFs")
	assert.NotContains(t, output, "ClusterVRF")
}

func TestRenderList(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)

	list := &networkv1alpha1.NodeNetworkConfigList{
		Items: []networkv1alpha1.NodeNetworkConfig{
			*testNNC(),
		},
	}

	r.RenderList(list)
	output := buf.String()

	assert.Contains(t, output, "NODE")
	assert.Contains(t, output, "REVISION")
	assert.Contains(t, output, "STATUS")
	assert.Contains(t, output, "worker-1")
	assert.Contains(t, output, "provisioned")
}

func TestRenderList_Empty(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)

	r.RenderList(&networkv1alpha1.NodeNetworkConfigList{})
	assert.Contains(t, buf.String(), "No NodeNetworkConfigs found")
}

func TestParseOrigins(t *testing.T) {
	t.Run("valid annotation", func(t *testing.T) {
		annotations := map[string]string{
			originAnnotation: `{"layer2s/prod": "Layer2Attachment/my-l2a"}`,
		}
		origins := ParseOrigins(annotations)
		assert.Equal(t, "Layer2Attachment/my-l2a", origins["layer2s/prod"])
	})

	t.Run("no annotation", func(t *testing.T) {
		origins := ParseOrigins(nil)
		assert.Nil(t, origins)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		annotations := map[string]string{originAnnotation: "not json"}
		origins := ParseOrigins(annotations)
		assert.Nil(t, origins)
	})
}

func TestColorStatus(t *testing.T) {
	r := New(nil, true)
	green := r.colorStatus("provisioned")
	assert.True(t, strings.Contains(green, "provisioned"))
	assert.True(t, strings.Contains(green, "\033[32m"))

	rNoColor := New(nil, false)
	plain := rNoColor.colorStatus("provisioned")
	assert.Equal(t, "provisioned", plain)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 10))
	assert.Equal(t, "abcdefghij…", truncate("abcdefghijklmnop", 10))
}
