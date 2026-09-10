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

package builder

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

func TestResolveSelectorVRFsStaysWithinNamespace(t *testing.T) {
	const (
		roleLabelKey = "role"
		roleClient   = "client"
	)
	ref := "shared-vrf"
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{roleLabelKey: roleClient}}
	vrfs := []nc.VRF{
		{ObjectMeta: metav1.ObjectMeta{Namespace: testTenantA, Name: ref}, Spec: nc.VRFSpec{VRF: "fabric-a"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: testTenantB, Name: ref}, Spec: nc.VRFSpec{VRF: "fabric-b"}},
	}
	destinations := []nc.Destination{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: testTenantA, Name: "shared-destination", Labels: map[string]string{roleLabelKey: roleClient}},
			Spec:       nc.DestinationSpec{VRFRef: &ref},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: testTenantB, Name: "shared-destination", Labels: map[string]string{roleLabelKey: roleClient}},
			Spec:       nc.DestinationSpec{VRFRef: &ref},
		},
	}
	vrfsByKey := resolver.ResolveVRFsByKey(vrfs)
	data := &resolver.ResolvedData{
		RawDestinations:   destinations,
		DestinationsByKey: resolver.ResolveDestinationsByKey(destinations, vrfsByKey),
	}

	got := resolveSelectorVRFs(testTenantA, selector, data)
	if len(got) != 1 || got["fabric-a"] == nil {
		t.Fatalf("resolveSelectorVRFs() = %#v, want only fabric-a", got)
	}
}
