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

package resolver

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

func TestNamespacedIndexesKeepDuplicateNamesIsolated(t *testing.T) {
	const (
		tenantA = "tenant-a"
		tenantB = "tenant-b"
	)
	ref := "shared-vrf"
	vrfs := []nc.VRF{
		{ObjectMeta: metav1.ObjectMeta{Namespace: tenantA, Name: ref}, Spec: nc.VRFSpec{VRF: "fabric-a"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: tenantB, Name: ref}, Spec: nc.VRFSpec{VRF: "fabric-b"}},
	}
	destinations := []nc.Destination{
		{ObjectMeta: metav1.ObjectMeta{Namespace: tenantA, Name: "shared-destination"}, Spec: nc.DestinationSpec{VRFRef: &ref}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: tenantB, Name: "shared-destination"}, Spec: nc.DestinationSpec{VRFRef: &ref}},
	}

	data := ResolvedData{
		VRFsByKey:         ResolveVRFsByKey(vrfs),
		DestinationsByKey: ResolveDestinationsByKey(destinations, ResolveVRFsByKey(vrfs)),
	}

	for namespace, wantVRF := range map[string]string{tenantA: "fabric-a", tenantB: "fabric-b"} {
		vrf, ok := data.VRF(namespace, ref)
		if !ok || vrf.Spec.VRF != wantVRF {
			t.Fatalf("VRF(%q, %q) = %#v, %v; want %q", namespace, ref, vrf, ok, wantVRF)
		}
		destination, ok := data.Destination(namespace, "shared-destination")
		if !ok || destination.VRFSpec == nil || destination.VRFSpec.VRF != wantVRF {
			t.Fatalf("Destination(%q) resolved VRF = %#v, %v; want %q", namespace, destination, ok, wantVRF)
		}
	}
}
