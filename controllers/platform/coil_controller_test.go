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

package platform

import (
	"context"
	"testing"
	"time"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = nc.AddToScheme(s)
	return s
}

func reconcileOnce(t *testing.T, r *CoilReconciler, name, namespace string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
}

func getIPPool(t *testing.T, r *CoilReconciler, name string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(calicoIPPoolGVK)
	err := r.Get(context.Background(), types.NamespacedName{Name: name}, obj)
	if err != nil {
		return nil
	}
	return obj
}

func getEgress(t *testing.T, r *CoilReconciler, name, namespace string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(coilEgressGVK)
	err := r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, obj)
	if err != nil {
		return nil
	}
	return obj
}

func TestCoilReconciler_CreateIPPoolsAndEgress(t *testing.T) {
	scheme := newScheme()
	replicas := int32(3)

	dest := &nc.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "dest1",
			Labels: map[string]string{"env": "prod"},
		},
		Spec: nc.DestinationSpec{
			Prefixes: []string{"10.102.0.0/24", "fda5:25c1:193c::/64"},
		},
	}

	ob := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "egress-a",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-1",
			Destinations: &metav1.LabelSelector{
				MatchLabels: map[string]string{"env": "prod"},
			},
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.200.0.0/28"},
				IPv6: []string{"fd00:200::/112"},
			},
			Replicas: &replicas,
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob, dest).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	// First reconcile adds finalizer.
	reconcileOnce(t, r, "egress-a", "default")
	// Second reconcile creates resources.
	reconcileOnce(t, r, "egress-a", "default")

	// Verify IPv4 IPPool.
	poolV4 := getIPPool(t, r, "egress-a-v4")
	if poolV4 == nil {
		t.Fatal("expected egress-a-v4 IPPool to exist")
	}
	cidr, _, _ := unstructured.NestedString(poolV4.Object, "spec", "cidr")
	if cidr != "10.200.0.0/28" {
		t.Errorf("expected cidr 10.200.0.0/28, got %s", cidr)
	}
	blockSize, _, _ := unstructured.NestedInt64(poolV4.Object, "spec", "blockSize")
	if blockSize != 32 {
		t.Errorf("expected blockSize 32, got %d", blockSize)
	}
	nodeSelector, _, _ := unstructured.NestedString(poolV4.Object, "spec", "nodeSelector")
	if nodeSelector != "!all()" {
		t.Errorf("expected nodeSelector '!all()', got %s", nodeSelector)
	}
	natOutgoing, _, _ := unstructured.NestedBool(poolV4.Object, "spec", "natOutgoing")
	if natOutgoing != false {
		t.Errorf("expected natOutgoing false, got %v", natOutgoing)
	}
	allowedUses, _, _ := unstructured.NestedStringSlice(poolV4.Object, "spec", "allowedUses")
	if len(allowedUses) != 1 || allowedUses[0] != "Workload" {
		t.Errorf("expected allowedUses [Workload], got %v", allowedUses)
	}

	// Verify IPv6 IPPool.
	poolV6 := getIPPool(t, r, "egress-a-v6")
	if poolV6 == nil {
		t.Fatal("expected egress-a-v6 IPPool to exist")
	}
	cidrV6, _, _ := unstructured.NestedString(poolV6.Object, "spec", "cidr")
	if cidrV6 != "fd00:200::/112" {
		t.Errorf("expected cidr fd00:200::/112, got %s", cidrV6)
	}
	blockSizeV6, _, _ := unstructured.NestedInt64(poolV6.Object, "spec", "blockSize")
	if blockSizeV6 != 128 {
		t.Errorf("expected blockSize 128, got %d", blockSizeV6)
	}

	// Verify Coil Egress.
	egress := getEgress(t, r, "egress-a", "default")
	if egress == nil {
		t.Fatal("expected egress-a Egress to exist")
	}
	destinations, _, _ := unstructured.NestedStringSlice(egress.Object, "spec", "destinations")
	if len(destinations) != 2 {
		t.Fatalf("expected 2 destinations, got %d", len(destinations))
	}
	egressReplicas, _, _ := unstructured.NestedInt64(egress.Object, "spec", "replicas")
	if egressReplicas != 3 {
		t.Errorf("expected replicas 3, got %d", egressReplicas)
	}
	v4Annotation, _, _ := unstructured.NestedString(egress.Object, "spec", "template", "metadata", "annotations", "cni.projectcalico.org/ipv4pools")
	if v4Annotation != `["egress-a-v4"]` {
		t.Errorf("expected ipv4pools annotation '[\"egress-a-v4\"]', got %s", v4Annotation)
	}
	v6Annotation, _, _ := unstructured.NestedString(egress.Object, "spec", "template", "metadata", "annotations", "cni.projectcalico.org/ipv6pools")
	if v6Annotation != `["egress-a-v6"]` {
		t.Errorf("expected ipv6pools annotation '[\"egress-a-v6\"]', got %s", v6Annotation)
	}

	// Verify managed-by label on IPPool.
	lbls := poolV4.GetLabels()
	if lbls["app.kubernetes.io/managed-by"] != "network-connector" {
		t.Errorf("expected managed-by label, got %v", lbls)
	}
	if lbls["network-connector.sylvaproject.org/outbound"] != "egress-a" {
		t.Errorf("expected outbound label, got %v", lbls)
	}
}

func TestCoilReconciler_ResolveMultipleDestinations(t *testing.T) {
	scheme := newScheme()

	dest1 := &nc.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "dest-a",
			Labels: map[string]string{"tier": "backend"},
		},
		Spec: nc.DestinationSpec{
			Prefixes: []string{"10.1.0.0/24"},
		},
	}
	dest2 := &nc.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "dest-b",
			Labels: map[string]string{"tier": "backend"},
		},
		Spec: nc.DestinationSpec{
			Prefixes: []string{"10.2.0.0/16", "fd00:2::/64"},
		},
	}
	// dest3 should NOT match.
	dest3 := &nc.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "dest-c",
			Labels: map[string]string{"tier": "frontend"},
		},
		Spec: nc.DestinationSpec{
			Prefixes: []string{"10.99.0.0/24"},
		},
	}

	ob := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "multi-dest",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-2",
			Destinations: &metav1.LabelSelector{
				MatchLabels: map[string]string{"tier": "backend"},
			},
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.200.1.0/28"},
			},
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob, dest1, dest2, dest3).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	reconcileOnce(t, r, "multi-dest", "default")
	reconcileOnce(t, r, "multi-dest", "default")

	egress := getEgress(t, r, "multi-dest", "default")
	if egress == nil {
		t.Fatal("expected multi-dest Egress to exist")
	}
	dests, _, _ := unstructured.NestedStringSlice(egress.Object, "spec", "destinations")
	if len(dests) != 3 {
		t.Fatalf("expected 3 destination prefixes (from dest-a + dest-b), got %d: %v", len(dests), dests)
	}

	// Should contain prefixes from dest-a and dest-b but not dest-c.
	found := map[string]bool{}
	for _, d := range dests {
		found[d] = true
	}
	for _, expected := range []string{"10.1.0.0/24", "10.2.0.0/16", "fd00:2::/64"} {
		if !found[expected] {
			t.Errorf("expected prefix %s not found in destinations: %v", expected, dests)
		}
	}
	if found["10.99.0.0/24"] {
		t.Error("destination from non-matching dest-c should not be present")
	}
}

func TestCoilReconciler_SkipIPv4Pool(t *testing.T) {
	scheme := newScheme()

	ob := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ipv6-only",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-3",
			Addresses: &nc.AddressAllocation{
				IPv6: []string{"fd00:300::/112"},
			},
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	reconcileOnce(t, r, "ipv6-only", "default")
	reconcileOnce(t, r, "ipv6-only", "default")

	// IPv4 pool should NOT exist.
	if pool := getIPPool(t, r, "ipv6-only-v4"); pool != nil {
		t.Error("expected no IPv4 IPPool for IPv6-only outbound")
	}

	// IPv6 pool should exist.
	if pool := getIPPool(t, r, "ipv6-only-v6"); pool == nil {
		t.Error("expected IPv6 IPPool to exist")
	}

	// Egress should only have IPv6 pool annotation.
	egress := getEgress(t, r, "ipv6-only", "default")
	if egress == nil {
		t.Fatal("expected Egress to exist")
	}
	v4Ann, _, _ := unstructured.NestedString(egress.Object, "spec", "template", "metadata", "annotations", "cni.projectcalico.org/ipv4pools")
	if v4Ann != "" {
		t.Errorf("expected no ipv4pools annotation, got %s", v4Ann)
	}
	v6Ann, _, _ := unstructured.NestedString(egress.Object, "spec", "template", "metadata", "annotations", "cni.projectcalico.org/ipv6pools")
	if v6Ann != `["ipv6-only-v6"]` {
		t.Errorf("expected ipv6pools annotation, got %s", v6Ann)
	}
}

func TestCoilReconciler_SkipIPv6Pool(t *testing.T) {
	scheme := newScheme()

	ob := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ipv4-only",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-4",
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.200.4.0/28"},
			},
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	reconcileOnce(t, r, "ipv4-only", "default")
	reconcileOnce(t, r, "ipv4-only", "default")

	if pool := getIPPool(t, r, "ipv4-only-v4"); pool == nil {
		t.Error("expected IPv4 IPPool to exist")
	}
	if pool := getIPPool(t, r, "ipv4-only-v6"); pool != nil {
		t.Error("expected no IPv6 IPPool for IPv4-only outbound")
	}
}

func TestCoilReconciler_DefaultReplicas(t *testing.T) {
	scheme := newScheme()

	ob := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-replicas",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-5",
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.200.5.0/28"},
			},
			// Replicas not set — should default to 1.
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	reconcileOnce(t, r, "no-replicas", "default")
	reconcileOnce(t, r, "no-replicas", "default")

	egress := getEgress(t, r, "no-replicas", "default")
	if egress == nil {
		t.Fatal("expected Egress to exist")
	}
	replicas, _, _ := unstructured.NestedInt64(egress.Object, "spec", "replicas")
	if replicas != 1 {
		t.Errorf("expected default replicas 1, got %d", replicas)
	}
}

func TestCoilReconciler_DeletionCleanup(t *testing.T) {
	scheme := newScheme()

	ob := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "to-delete",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-6",
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.200.6.0/28"},
				IPv6: []string{"fd00:600::/112"},
			},
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	// Create resources.
	reconcileOnce(t, r, "to-delete", "default")
	reconcileOnce(t, r, "to-delete", "default")

	// Verify resources exist.
	if getIPPool(t, r, "to-delete-v4") == nil {
		t.Fatal("expected v4 IPPool to exist before deletion")
	}
	if getIPPool(t, r, "to-delete-v6") == nil {
		t.Fatal("expected v6 IPPool to exist before deletion")
	}
	if getEgress(t, r, "to-delete", "default") == nil {
		t.Fatal("expected Egress to exist before deletion")
	}

	// Mark for deletion.
	freshOb := &nc.Outbound{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "to-delete", Namespace: "default"}, freshOb); err != nil {
		t.Fatalf("error fetching outbound: %v", err)
	}
	now := metav1.NewTime(time.Now())
	freshOb.DeletionTimestamp = &now
	// The fake client doesn't allow setting DeletionTimestamp via Update,
	// so we simulate by creating a new client with the modified object.
	cli = fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOb).Build()
	r.Client = cli

	// Reconcile deletion.
	reconcileOnce(t, r, "to-delete", "default")

	// Verify resources are deleted.
	if getIPPool(t, r, "to-delete-v4") != nil {
		t.Error("expected v4 IPPool to be deleted")
	}
	if getIPPool(t, r, "to-delete-v6") != nil {
		t.Error("expected v6 IPPool to be deleted")
	}
	if getEgress(t, r, "to-delete", "default") != nil {
		t.Error("expected Egress to be deleted")
	}

	// The fake client removes the object once all finalizers are cleared
	// with a DeletionTimestamp set — this is correct K8s behavior.
	// Verify the outbound was fully cleaned up.
	updatedOb := &nc.Outbound{}
	err := r.Get(context.Background(), types.NamespacedName{Name: "to-delete", Namespace: "default"}, updatedOb)
	if err == nil {
		// Object still exists — verify finalizer was removed.
		for _, f := range updatedOb.Finalizers {
			if f == coilFinalizer {
				t.Error("expected finalizer to be removed")
			}
		}
	}
	// If err is NotFound, the object was fully deleted — that's also correct.
}

func TestCoilReconciler_DestinationChangeTriggersUpdate(t *testing.T) {
	scheme := newScheme()

	dest := &nc.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "changing-dest",
			Labels: map[string]string{"app": "web"},
		},
		Spec: nc.DestinationSpec{
			Prefixes: []string{"10.50.0.0/24"},
		},
	}

	ob := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dest-update",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-7",
			Destinations: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.200.7.0/28"},
			},
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob, dest).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	// Initial reconcile.
	reconcileOnce(t, r, "dest-update", "default")
	reconcileOnce(t, r, "dest-update", "default")

	egress := getEgress(t, r, "dest-update", "default")
	if egress == nil {
		t.Fatal("expected Egress to exist")
	}
	dests1, _, _ := unstructured.NestedStringSlice(egress.Object, "spec", "destinations")
	if len(dests1) != 1 || dests1[0] != "10.50.0.0/24" {
		t.Fatalf("expected [10.50.0.0/24], got %v", dests1)
	}

	// Update destination prefixes.
	updatedDest := &nc.Destination{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "changing-dest"}, updatedDest); err != nil {
		t.Fatalf("error fetching destination: %v", err)
	}
	updatedDest.Spec.Prefixes = []string{"10.50.0.0/24", "10.51.0.0/24"}
	if err := r.Update(context.Background(), updatedDest); err != nil {
		t.Fatalf("error updating destination: %v", err)
	}

	// Re-reconcile the Outbound (simulating the watch trigger).
	reconcileOnce(t, r, "dest-update", "default")

	egress = getEgress(t, r, "dest-update", "default")
	if egress == nil {
		t.Fatal("expected Egress to exist after update")
	}
	dests2, _, _ := unstructured.NestedStringSlice(egress.Object, "spec", "destinations")
	if len(dests2) != 2 {
		t.Fatalf("expected 2 destinations after update, got %d: %v", len(dests2), dests2)
	}
	found := map[string]bool{}
	for _, d := range dests2 {
		found[d] = true
	}
	if !found["10.51.0.0/24"] {
		t.Errorf("expected new prefix 10.51.0.0/24, got %v", dests2)
	}
}

func TestCoilReconciler_NoMatchingDestinations(t *testing.T) {
	scheme := newScheme()

	// Destination with different labels — won't match.
	dest := &nc.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "unrelated",
			Labels: map[string]string{"team": "infra"},
		},
		Spec: nc.DestinationSpec{
			Prefixes: []string{"10.99.0.0/24"},
		},
	}

	ob := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-match",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-8",
			Destinations: &metav1.LabelSelector{
				MatchLabels: map[string]string{"team": "platform"},
			},
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.200.8.0/28"},
			},
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob, dest).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	reconcileOnce(t, r, "no-match", "default")
	reconcileOnce(t, r, "no-match", "default")

	egress := getEgress(t, r, "no-match", "default")
	if egress == nil {
		t.Fatal("expected Egress to exist even with no matching destinations")
	}
	dests, _, _ := unstructured.NestedStringSlice(egress.Object, "spec", "destinations")
	if len(dests) != 0 {
		t.Errorf("expected empty destinations, got %v", dests)
	}
}

func TestCoilReconciler_StatusAddressesPreferred(t *testing.T) {
	scheme := newScheme()

	ob := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "status-pref",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-9",
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.200.9.0/28"},
			},
		},
		Status: nc.OutboundStatus{
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.200.99.0/28"},
			},
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob).WithStatusSubresource(ob).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	reconcileOnce(t, r, "status-pref", "default")
	reconcileOnce(t, r, "status-pref", "default")

	pool := getIPPool(t, r, "status-pref-v4")
	if pool == nil {
		t.Fatal("expected IPv4 pool to exist")
	}
	cidr, _, _ := unstructured.NestedString(pool.Object, "spec", "cidr")
	if cidr != "10.200.99.0/28" {
		t.Errorf("expected status address 10.200.99.0/28, got %s", cidr)
	}
}

func TestCoilReconciler_MapDestinationToOutbounds(t *testing.T) {
	scheme := newScheme()

	ob1 := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ob-match",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-10",
			Destinations: &metav1.LabelSelector{
				MatchLabels: map[string]string{"zone": "a"},
			},
		},
	}
	ob2 := &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ob-nomatch",
			Namespace: "default",
		},
		Spec: nc.OutboundSpec{
			NetworkRef: "net-11",
			Destinations: &metav1.LabelSelector{
				MatchLabels: map[string]string{"zone": "b"},
			},
		},
	}

	dest := &nc.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "dest-zone-a",
			Labels: map[string]string{"zone": "a"},
		},
		Spec: nc.DestinationSpec{
			Prefixes: []string{"10.10.0.0/24"},
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ob1, ob2, dest).Build()
	r := &CoilReconciler{Client: cli, Scheme: scheme}

	requests := r.mapDestinationToOutbounds(context.Background(), dest)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Name != "ob-match" {
		t.Errorf("expected ob-match, got %s", requests[0].Name)
	}
}
