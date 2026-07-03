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

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

func TestEffectiveInterfaceName(t *testing.T) {
	vlan1000 := int32(1000)
	override := "chris-l2"
	empty := ""

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
			name: "network without VLAN yields empty",
			spec: nc.Layer2AttachmentSpec{NetworkRef: "net-no-vlan"},
			want: "",
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
