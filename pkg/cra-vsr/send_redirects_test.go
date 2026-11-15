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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telekom/das-schiff-network-operator/pkg/helpers/types"
)

func newRedirectsTestNS() *Namespace {
	return &Namespace{
		Interfaces: &Interfaces{
			Bridges: []Bridge{{Name: "l2.501", NetworkStack: &NetworkStack{
				IPv4: &NetworkStackV4{AcceptARP: types.ToPtr("always")},
			}}},
			VXLANs: []VXLAN{{Name: "vx.501"}},
			VLANs:  []VLAN{{Name: "vlan.501"}},
		},
		VRFs: []VRF{
			{Name: "a", Interfaces: &Interfaces{Bridges: []Bridge{{Name: "br.a"}}, VXLANs: []VXLAN{{Name: "vx.a"}}}},
			{Name: "b"},
		},
	}
}

func redirectStacks(ns *Namespace) map[string]*NetworkStack {
	stacks := map[string]*NetworkStack{}
	for _, intfs := range []*Interfaces{ns.Interfaces, ns.VRFs[0].Interfaces} {
		for i := range intfs.Bridges {
			stacks[intfs.Bridges[i].Name] = intfs.Bridges[i].NetworkStack
		}
		for i := range intfs.VXLANs {
			stacks[intfs.VXLANs[i].Name] = intfs.VXLANs[i].NetworkStack
		}
		for i := range intfs.VLANs {
			stacks[intfs.VLANs[i].Name] = intfs.VLANs[i].NetworkStack
		}
	}
	return stacks
}

func TestDisableSendRedirects(t *testing.T) {
	t.Setenv("NWOP_SEND_REDIRECTS", "")
	ns := newRedirectsTestNS()
	disableSendRedirects(ns)

	stacks := redirectStacks(ns)
	require.Len(t, stacks, 5)
	for name, stack := range stacks {
		require.NotNil(t, stack, name)
		require.NotNil(t, stack.IPv4, name)
		require.Equal(t, types.ToPtr(false), stack.IPv4.SendRedirects, name)
	}
	require.Equal(t, types.ToPtr("always"), stacks["l2.501"].IPv4.AcceptARP, "other leaves are kept")
}

func TestDisableSendRedirectsOptOut(t *testing.T) {
	t.Setenv("NWOP_SEND_REDIRECTS", "true")
	ns := newRedirectsTestNS()
	disableSendRedirects(ns)

	for name, stack := range redirectStacks(ns) {
		if stack != nil && stack.IPv4 != nil {
			require.Nil(t, stack.IPv4.SendRedirects, name)
		}
	}
}
