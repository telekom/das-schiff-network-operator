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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

func TestTemplateFRRLastResort(t *testing.T) {
	vrf := "combo"
	address := "192.0.2.1"
	routes := []v1alpha1.StaticRoute{
		{Prefix: "192.0.2.0/24", LastResort: true},
		{Prefix: "2001:db8::/64", LastResort: true},
		{Prefix: "198.51.100.0/24"},
		{Prefix: "2001:db8:1::/64"},
		{Prefix: "203.0.113.0/24", NextHop: &v1alpha1.NextHop{Address: &address}},
		{Prefix: "2001:db8:2::/64", NextHop: &v1alpha1.NextHop{Vrf: &vrf}},
	}
	tpl := FRRTemplate{FRRTemplatePath: filepath.Join("..", "..", "config", "agent-cra-frr", "frr.conf.tpl")}
	for _, tt := range []struct {
		name string
		spec v1alpha1.NodeNetworkConfigSpec
	}{
		{name: "cluster", spec: v1alpha1.NodeNetworkConfigSpec{ClusterVRF: &v1alpha1.VRF{StaticRoutes: routes}}},
		{name: "fabric", spec: v1alpha1.NodeNetworkConfigSpec{FabricVRFs: map[string]v1alpha1.FabricVRF{
			"tenant": {VRF: v1alpha1.VRF{StaticRoutes: routes}},
		}}},
		{name: "local", spec: v1alpha1.NodeNetworkConfigSpec{LocalVRFs: map[string]v1alpha1.VRF{
			"tenant": {StaticRoutes: routes},
		}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rendered, err := tpl.TemplateFRR(&config.BaseConfig{}, &tt.spec)
			require.NoError(t, err)
			var commands []string
			for _, line := range strings.Split(rendered, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "ip route ") || strings.HasPrefix(line, "ipv6 route ") {
					commands = append(commands, line)
				}
			}
			for _, want := range []string{
				"ip route 192.0.2.0/24 blackhole 254",
				"ipv6 route 2001:db8::/64 blackhole 254",
				"ip route 198.51.100.0/24 blackhole",
				"ipv6 route 2001:db8:1::/64 blackhole",
				"ip route 203.0.113.0/24 192.0.2.1",
				"ipv6 route 2001:db8:2::/64 combo nexthop-vrf combo",
			} {
				assert.Contains(t, commands, want)
			}
		})
	}
}
