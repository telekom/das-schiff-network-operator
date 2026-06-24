/*
Copyright 2025.

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

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

func TestFlipMirrorDirection(t *testing.T) {
	cases := map[string]string{
		"ingress": "egress",
		"egress":  "ingress",
		"":        "",
		"both":    "both",
	}
	for in, want := range cases {
		if got := flipMirrorDirection(in); got != want {
			t.Errorf("flipMirrorDirection(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateMirrorTrafficDirectionAndFrom(t *testing.T) {
	mgr := &Manager{}
	acl := &v1alpha1.MirrorACL{
		MirrorDestination: "gre.collector",
		Direction:         v1alpha1.MirrorDirectionIngress,
	}

	// Layer2 access port: workload-facing, so the caller flips the direction and
	// binds to vlan.<id>.
	t.Run("layer2 access port flips direction", func(t *testing.T) {
		ns := &Namespace{}
		mgr.createMirrorTraffic(ns, "vlan.501", flipMirrorDirection(string(acl.Direction)), acl)
		if ns.MTraffic == nil || len(ns.MTraffic.Actions) != 1 {
			t.Fatalf("expected one mirror-traffic action, got %+v", ns.MTraffic)
		}
		action := ns.MTraffic.Actions[0]
		if action.From != "vlan.501" {
			t.Errorf("From = %q, want vlan.501", action.From)
		}
		// ingress (to-workload) on a workload-facing port -> egress hook.
		if action.Direction != "egress" {
			t.Errorf("Direction = %q, want egress (flipped)", action.Direction)
		}
	})

	// Fabric-facing VRF port: no flip, bound to vx.<vrf>.
	t.Run("fabric vxlan port keeps direction", func(t *testing.T) {
		ns := &Namespace{}
		mgr.createMirrorTraffic(ns, "vx.cluster", string(acl.Direction), acl)
		action := ns.MTraffic.Actions[0]
		if action.From != "vx.cluster" {
			t.Errorf("From = %q, want vx.cluster", action.From)
		}
		if action.Direction != "ingress" {
			t.Errorf("Direction = %q, want ingress (unchanged)", action.Direction)
		}
	})
}
