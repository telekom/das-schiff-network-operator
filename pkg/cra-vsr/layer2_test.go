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

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

func renderAcceptGARP(t *testing.T, accept bool) string {
	t.Helper()
	ns := &Namespace{Name: "hbn", Interfaces: &Interfaces{}}
	l2 := NewLayer2(&v1alpha1.NodeNetworkConfigSpec{Layer2s: map[string]v1alpha1.Layer2{
		"501": {VLAN: 501, VNI: 4000002, MTU: 1500, AcceptGratuitousARP: accept},
	}}, ns, manager)
	require.NoError(t, l2.setup())
	require.Len(t, ns.Interfaces.Bridges, 1)
	stack := ns.Interfaces.Bridges[0].NetworkStack
	require.NotNil(t, stack)
	require.NotNil(t, stack.IPv4)
	require.NotNil(t, stack.IPv4.AcceptARP)
	return *stack.IPv4.AcceptARP
}

func TestLayer2AcceptGratuitousARP(t *testing.T) {
	for _, tt := range []struct {
		name   string
		accept bool
		env    string
		want   string
	}{
		{name: "default rejects", want: "never"},
		{name: "network opts in", accept: true, want: "always"},
		{name: "variable restores old default", env: "always", want: "always"},
		{name: "unknown variable value keeps default", env: "true", want: "never"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NWOP_L2_ACCEPT_GARP", tt.env)
			require.Equal(t, tt.want, renderAcceptGARP(t, tt.accept))
		})
	}
}

// The leaf is always rendered, so the merge edit in ApplyConfiguration
// overwrites a bridge that a previous reconcile set to "always".
func TestLayer2AcceptGratuitousARPReconcile(t *testing.T) {
	t.Setenv("NWOP_L2_ACCEPT_GARP", "")
	require.Equal(t, "always", renderAcceptGARP(t, true))
	require.Equal(t, "never", renderAcceptGARP(t, false))
}
