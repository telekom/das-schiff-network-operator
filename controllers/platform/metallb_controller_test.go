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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

func ptrStr(s string) *string { return &s }

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	nc.AddToScheme(s) //nolint:errcheck
	return s
}

func newTestReconciler(objs ...client.Object) (*MetalLBReconciler, client.Client) {
	scheme := newTestScheme()
	cb := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&nc.Inbound{})
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	c := cb.Build()
	r := &MetalLBReconciler{Client: c, Scheme: scheme}
	return r, c
}

func newInbound(name string, spec nc.InboundSpec) *nc.Inbound {
	return &nc.Inbound{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       spec,
	}
}

func getUnstructured(t *testing.T, c client.Client, gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: metallbNamespace}, obj)
	if err != nil {
		t.Fatalf("failed to get %s %s/%s: %v", gvk.Kind, metallbNamespace, name, err)
	}
	return obj
}

var (
	ipPoolGVK = schema.GroupVersionKind{Group: "metallb.io", Version: "v1beta1", Kind: "IPAddressPool"}
	bgpGVK    = schema.GroupVersionKind{Group: "metallb.io", Version: "v1beta1", Kind: "BGPAdvertisement"}
	l2GVK     = schema.GroupVersionKind{Group: "metallb.io", Version: "v1beta1", Kind: "L2Advertisement"}
)

func doReconcile(t *testing.T, r *MetalLBReconciler, name string) {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.Requeue {
		t.Fatal("unexpected requeue")
	}
}

func TestMetalLBReconciler_BGPAdvertisement(t *testing.T) {
	ib := newInbound("test-bgp", nc.InboundSpec{
		NetworkRef:    "net1",
		Addresses:     &nc.AddressAllocation{IPv4: []string{"10.0.0.0/24"}},
		Advertisement: nc.AdvertisementConfig{Type: "bgp"},
	})
	r, c := newTestReconciler(ib)
	doReconcile(t, r, "test-bgp")

	pool := getUnstructured(t, c, ipPoolGVK, "test-bgp")
	addrs, _, _ := unstructured.NestedSlice(pool.Object, "spec", "addresses")
	if len(addrs) != 1 || addrs[0] != "10.0.0.0/24" {
		t.Errorf("expected addresses [10.0.0.0/24], got %v", addrs)
	}
	if pool.GetLabels()["app.kubernetes.io/managed-by"] != managedByValue {
		t.Error("missing managed-by label on IPAddressPool")
	}

	adv := &unstructured.Unstructured{}
	adv.SetGroupVersionKind(bgpGVK)
	err := c.Get(context.Background(), types.NamespacedName{Name: "test-bgp", Namespace: metallbNamespace}, adv)
	if err == nil {
		t.Error("expected NO BGPAdvertisement (kube-vip handles BGP advertisement)")
	}
}

func TestMetalLBReconciler_L2Advertisement(t *testing.T) {
	ib := newInbound("test-l2", nc.InboundSpec{
		NetworkRef:    "net1",
		Addresses:     &nc.AddressAllocation{IPv4: []string{"192.168.1.0/24"}},
		Advertisement: nc.AdvertisementConfig{Type: "l2"},
	})
	r, c := newTestReconciler(ib)
	doReconcile(t, r, "test-l2")

	getUnstructured(t, c, ipPoolGVK, "test-l2")

	adv := getUnstructured(t, c, l2GVK, "test-l2")
	pools, _, _ := unstructured.NestedStringSlice(adv.Object, "spec", "ipAddressPools")
	if len(pools) != 1 || pools[0] != "test-l2" {
		t.Errorf("expected L2Advertisement ipAddressPools=[test-l2], got %v", pools)
	}
	if adv.GetLabels()["app.kubernetes.io/managed-by"] != managedByValue {
		t.Error("missing managed-by label on L2Advertisement")
	}
}

func TestMetalLBReconciler_CustomPoolName(t *testing.T) {
	ib := newInbound("test-custom", nc.InboundSpec{
		NetworkRef:    "net1",
		PoolName:      ptrStr("my-custom-pool"),
		Addresses:     &nc.AddressAllocation{IPv4: []string{"10.1.0.0/24"}},
		Advertisement: nc.AdvertisementConfig{Type: "bgp"},
	})
	r, c := newTestReconciler(ib)
	doReconcile(t, r, "test-custom")

	getUnstructured(t, c, ipPoolGVK, "my-custom-pool")

	adv := &unstructured.Unstructured{}
	adv.SetGroupVersionKind(bgpGVK)
	err := c.Get(context.Background(), types.NamespacedName{Name: "my-custom-pool", Namespace: metallbNamespace}, adv)
	if err == nil {
		t.Error("expected NO BGPAdvertisement")
	}

	updated := &nc.Inbound{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-custom", Namespace: "default"}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.PoolName == nil || *updated.Status.PoolName != "my-custom-pool" {
		t.Errorf("expected status.poolName=my-custom-pool, got %v", updated.Status.PoolName)
	}
}

// Test 4: Prefer status addresses over spec addresses.
func TestMetalLBReconciler_PreferStatusAddresses(t *testing.T) {
	ib := newInbound("test-status-addr", nc.InboundSpec{
		NetworkRef:    "net1",
		Addresses:     &nc.AddressAllocation{IPv4: []string{"10.0.0.0/24"}},
		Advertisement: nc.AdvertisementConfig{Type: "bgp"},
	})
	ib.Status.Addresses = &nc.AddressAllocation{IPv4: []string{"10.99.0.0/24"}}
	r, c := newTestReconciler(ib)
	doReconcile(t, r, "test-status-addr")

	pool := getUnstructured(t, c, ipPoolGVK, "test-status-addr")
	addrs, _, _ := unstructured.NestedSlice(pool.Object, "spec", "addresses")
	if len(addrs) != 1 || addrs[0] != "10.99.0.0/24" {
		t.Errorf("expected status addresses [10.99.0.0/24], got %v", addrs)
	}
}

// Test 5: Update existing resources when Inbound spec changes.
func TestMetalLBReconciler_Update(t *testing.T) {
	ib := newInbound("test-update", nc.InboundSpec{
		NetworkRef:    "net1",
		Addresses:     &nc.AddressAllocation{IPv4: []string{"10.0.0.0/24"}},
		Advertisement: nc.AdvertisementConfig{Type: "bgp"},
	})
	r, c := newTestReconciler(ib)
	doReconcile(t, r, "test-update")

	// Update addresses on the Inbound.
	updated := &nc.Inbound{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-update", Namespace: "default"}, updated); err != nil {
		t.Fatal(err)
	}
	updated.Spec.Addresses = &nc.AddressAllocation{IPv4: []string{"10.1.0.0/24", "10.2.0.0/24"}}
	if err := c.Update(context.Background(), updated); err != nil {
		t.Fatal(err)
	}

	// Reconcile again.
	doReconcile(t, r, "test-update")

	pool := getUnstructured(t, c, ipPoolGVK, "test-update")
	addrs, _, _ := unstructured.NestedSlice(pool.Object, "spec", "addresses")
	if len(addrs) != 2 {
		t.Errorf("expected 2 addresses after update, got %d: %v", len(addrs), addrs)
	}
}

func TestMetalLBReconciler_Deletion(t *testing.T) {
	ib := newInbound("test-delete", nc.InboundSpec{
		NetworkRef:    "net1",
		Addresses:     &nc.AddressAllocation{IPv4: []string{"10.0.0.0/24"}},
		Advertisement: nc.AdvertisementConfig{Type: "bgp"},
	})
	r, c := newTestReconciler(ib)

	doReconcile(t, r, "test-delete")

	getUnstructured(t, c, ipPoolGVK, "test-delete")

	updated := &nc.Inbound{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-delete", Namespace: "default"}, updated); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), updated); err != nil {
		t.Fatal(err)
	}

	doReconcile(t, r, "test-delete")

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(ipPoolGVK)
	err := c.Get(context.Background(), types.NamespacedName{Name: "test-delete", Namespace: metallbNamespace}, obj)
	if err == nil {
		t.Error("expected IPAddressPool to be deleted")
	}

	advObj := &unstructured.Unstructured{}
	advObj.SetGroupVersionKind(l2GVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-delete", Namespace: metallbNamespace}, advObj); err == nil {
		t.Error("expected L2Advertisement to be deleted")
	}

	finalIb := &nc.Inbound{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-delete", Namespace: "default"}, finalIb); err != nil {
		return
	}
	for _, f := range finalIb.Finalizers {
		if f == metallbFinalizer {
			t.Error("finalizer should have been removed")
		}
	}
}

// Test 7: Dual-stack addresses (IPv4 + IPv6).
func TestMetalLBReconciler_DualStack(t *testing.T) {
	ib := newInbound("test-dual", nc.InboundSpec{
		NetworkRef: "net1",
		Addresses: &nc.AddressAllocation{
			IPv4: []string{"10.250.4.0/24"},
			IPv6: []string{"fdbb:6b17:90ba::/64"},
		},
		Advertisement: nc.AdvertisementConfig{Type: "bgp"},
	})
	r, c := newTestReconciler(ib)
	doReconcile(t, r, "test-dual")

	pool := getUnstructured(t, c, ipPoolGVK, "test-dual")
	addrs, _, _ := unstructured.NestedSlice(pool.Object, "spec", "addresses")
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses (dual-stack), got %d: %v", len(addrs), addrs)
	}
	if addrs[0] != "10.250.4.0/24" {
		t.Errorf("expected first address 10.250.4.0/24, got %v", addrs[0])
	}
	if addrs[1] != "fdbb:6b17:90ba::/64" {
		t.Errorf("expected second address fdbb:6b17:90ba::/64, got %v", addrs[1])
	}
}
