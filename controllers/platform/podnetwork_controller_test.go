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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

func reconcilePodNetworkOnce(t *testing.T, r *PodNetworkReconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
}

func getPodNetworkPool(t *testing.T, r *PodNetworkReconciler, name string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(calicoIPPoolGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Name: name}, obj); err != nil {
		return nil
	}
	return obj
}

func listPodNetworkPools(t *testing.T, r *PodNetworkReconciler, podnetwork, family string) []unstructured.Unstructured {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(calicoIPPoolGVK)
	sel := client.MatchingLabels{"network-connector.sylvaproject.org/podnetwork": podnetwork}
	if family != "" {
		sel["network-connector.sylvaproject.org/family"] = family
	}
	if err := r.List(context.Background(), list, sel); err != nil {
		t.Fatalf("error listing IPPools: %v", err)
	}
	return list.Items
}

func newPodNetworkReconciler(objs ...client.Object) *PodNetworkReconciler {
	scheme := newScheme()
	pnObjs := make([]client.Object, 0, len(objs))
	statusObjs := make([]client.Object, 0)
	for _, o := range objs {
		pnObjs = append(pnObjs, o)
		if _, ok := o.(*nc.PodNetwork); ok {
			statusObjs = append(statusObjs, o)
		}
	}
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pnObjs...)
	if len(statusObjs) > 0 {
		builder = builder.WithStatusSubresource(statusObjs...)
	}
	cli := builder.Build()
	return &PodNetworkReconciler{Client: cli, APIReader: cli, Scheme: scheme}
}

func TestPodNetworkReconciler_CreatesDualStackPools(t *testing.T) {
	network := &nc.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "net-pods", Namespace: "default"},
		Spec: nc.NetworkSpec{
			IPv4: &nc.IPNetwork{CIDR: "10.244.0.0/16"},
			IPv6: &nc.IPNetwork{CIDR: "fd00:10:244::/48"},
		},
	}
	pn := &nc.PodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-pods", Namespace: "default"},
		Spec:       nc.PodNetworkSpec{NetworkRef: "net-pods"},
	}

	r := newPodNetworkReconciler(pn, network)

	// First reconcile adds finalizer, second creates pools.
	reconcilePodNetworkOnce(t, r, "tenant-pods")
	reconcilePodNetworkOnce(t, r, "tenant-pods")

	v4Name := podNetworkPoolName("default", "tenant-pods", "v4", "10.244.0.0/16")
	poolV4 := getPodNetworkPool(t, r, v4Name)
	if poolV4 == nil {
		t.Fatal("expected IPv4 IPPool to exist")
	}
	if cidr, _, _ := unstructured.NestedString(poolV4.Object, "spec", "cidr"); cidr != "10.244.0.0/16" {
		t.Errorf("expected cidr 10.244.0.0/16, got %s", cidr)
	}
	if nat, _, _ := unstructured.NestedBool(poolV4.Object, "spec", "natOutgoing"); nat {
		t.Error("expected natOutgoing false")
	}
	if bs, _, _ := unstructured.NestedInt64(poolV4.Object, "spec", "blockSize"); bs != defaultIPv4BlockSize {
		t.Errorf("expected blockSize %d, got %d", defaultIPv4BlockSize, bs)
	}
	if ns, _, _ := unstructured.NestedString(poolV4.Object, "spec", "nodeSelector"); ns != "!all()" {
		t.Errorf("expected nodeSelector '!all()', got %s", ns)
	}
	if uses, _, _ := unstructured.NestedStringSlice(poolV4.Object, "spec", "allowedUses"); len(uses) != 1 || uses[0] != "Workload" {
		t.Errorf("expected allowedUses [Workload], got %v", uses)
	}
	lbls := poolV4.GetLabels()
	if lbls["app.kubernetes.io/managed-by"] != managedByValue {
		t.Errorf("expected managed-by label, got %v", lbls)
	}
	if lbls["network-connector.sylvaproject.org/podnetwork"] != "tenant-pods" {
		t.Errorf("expected podnetwork label, got %v", lbls)
	}

	v6Name := podNetworkPoolName("default", "tenant-pods", "v6", "fd00:10:244::/48")
	poolV6 := getPodNetworkPool(t, r, v6Name)
	if poolV6 == nil {
		t.Fatal("expected IPv6 IPPool to exist")
	}
	if bs, _, _ := unstructured.NestedInt64(poolV6.Object, "spec", "blockSize"); bs != defaultIPv6BlockSize {
		t.Errorf("expected blockSize %d, got %d", defaultIPv6BlockSize, bs)
	}

	// Status must list both pool names, sorted.
	updated := &nc.PodNetwork{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-pods", Namespace: "default"}, updated); err != nil {
		t.Fatalf("error fetching podnetwork: %v", err)
	}
	if len(updated.Status.IPPools) != 2 {
		t.Fatalf("expected 2 ipPools in status, got %v", updated.Status.IPPools)
	}
	want := []string{v4Name, v6Name}
	// podNetworkPoolName produces deterministic values; ensure both present.
	got := map[string]bool{updated.Status.IPPools[0]: true, updated.Status.IPPools[1]: true}
	for _, w := range want {
		if !got[w] {
			t.Errorf("expected status.ipPools to contain %s, got %v", w, updated.Status.IPPools)
		}
	}
}

func TestPodNetworkReconciler_IPv4Only(t *testing.T) {
	network := &nc.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "net-v4", Namespace: "default"},
		Spec:       nc.NetworkSpec{IPv4: &nc.IPNetwork{CIDR: "10.10.0.0/24"}},
	}
	pn := &nc.PodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "v4-pods", Namespace: "default"},
		Spec:       nc.PodNetworkSpec{NetworkRef: "net-v4"},
	}

	r := newPodNetworkReconciler(pn, network)
	reconcilePodNetworkOnce(t, r, "v4-pods")
	reconcilePodNetworkOnce(t, r, "v4-pods")

	if pools := listPodNetworkPools(t, r, "v4-pods", "v4"); len(pools) != 1 {
		t.Errorf("expected 1 IPv4 pool, got %d", len(pools))
	}
	if pools := listPodNetworkPools(t, r, "v4-pods", "v6"); len(pools) != 0 {
		t.Errorf("expected no IPv6 pool, got %d", len(pools))
	}
}

func TestPodNetworkReconciler_MissingNetwork(t *testing.T) {
	pn := &nc.PodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "default"},
		Spec:       nc.PodNetworkSpec{NetworkRef: "does-not-exist"},
	}

	r := newPodNetworkReconciler(pn)
	reconcilePodNetworkOnce(t, r, "orphan")
	reconcilePodNetworkOnce(t, r, "orphan")

	if pools := listPodNetworkPools(t, r, "orphan", ""); len(pools) != 0 {
		t.Errorf("expected no pools when Network is missing, got %d", len(pools))
	}
	updated := &nc.PodNetwork{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "orphan", Namespace: "default"}, updated); err != nil {
		t.Fatalf("error fetching podnetwork: %v", err)
	}
	if len(updated.Status.IPPools) != 0 {
		t.Errorf("expected empty status.ipPools, got %v", updated.Status.IPPools)
	}
}

func TestPodNetworkReconciler_PrunesRemovedFamily(t *testing.T) {
	network := &nc.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "net-shrink", Namespace: "default"},
		Spec: nc.NetworkSpec{
			IPv4: &nc.IPNetwork{CIDR: "10.20.0.0/16"},
			IPv6: &nc.IPNetwork{CIDR: "fd00:20::/48"},
		},
	}
	pn := &nc.PodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "shrink-pods", Namespace: "default"},
		Spec:       nc.PodNetworkSpec{NetworkRef: "net-shrink"},
	}

	r := newPodNetworkReconciler(pn, network)
	reconcilePodNetworkOnce(t, r, "shrink-pods")
	reconcilePodNetworkOnce(t, r, "shrink-pods")

	if pools := listPodNetworkPools(t, r, "shrink-pods", ""); len(pools) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(pools))
	}

	// Remove the IPv6 family from the Network.
	fresh := &nc.Network{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "net-shrink", Namespace: "default"}, fresh); err != nil {
		t.Fatalf("error fetching network: %v", err)
	}
	fresh.Spec.IPv6 = nil
	if err := r.Update(context.Background(), fresh); err != nil {
		t.Fatalf("error updating network: %v", err)
	}

	reconcilePodNetworkOnce(t, r, "shrink-pods")

	if pools := listPodNetworkPools(t, r, "shrink-pods", "v6"); len(pools) != 0 {
		t.Errorf("expected IPv6 pool pruned, got %d", len(pools))
	}
	if pools := listPodNetworkPools(t, r, "shrink-pods", "v4"); len(pools) != 1 {
		t.Errorf("expected IPv4 pool retained, got %d", len(pools))
	}
}

func TestPodNetworkReconciler_DeletionCleanup(t *testing.T) {
	network := &nc.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "net-del", Namespace: "default"},
		Spec:       nc.NetworkSpec{IPv4: &nc.IPNetwork{CIDR: "10.30.0.0/16"}},
	}
	pn := &nc.PodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "del-pods", Namespace: "default"},
		Spec:       nc.PodNetworkSpec{NetworkRef: "net-del"},
	}

	r := newPodNetworkReconciler(pn, network)
	reconcilePodNetworkOnce(t, r, "del-pods")
	reconcilePodNetworkOnce(t, r, "del-pods")

	if pools := listPodNetworkPools(t, r, "del-pods", ""); len(pools) == 0 {
		t.Fatal("expected pools to exist before deletion")
	}

	fresh := &nc.PodNetwork{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "del-pods", Namespace: "default"}, fresh); err != nil {
		t.Fatalf("error fetching podnetwork: %v", err)
	}
	now := metav1.NewTime(time.Now())
	fresh.DeletionTimestamp = &now
	// The fake client does not allow setting DeletionTimestamp via Update, so
	// rebuild the client with the modified object (mirrors coil_controller_test).
	scheme := newScheme()
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fresh, network).Build()
	r.Client = cli
	r.APIReader = cli

	reconcilePodNetworkOnce(t, r, "del-pods")

	if pools := listPodNetworkPools(t, r, "del-pods", ""); len(pools) != 0 {
		t.Errorf("expected pools to be deleted, got %d", len(pools))
	}
}

func TestPodNetworkPoolName(t *testing.T) {
	n1 := podNetworkPoolName("default", "pn-a", "v4", "10.0.0.0/16")
	n2 := podNetworkPoolName("default", "pn-a", "v4", "10.0.0.0/16")
	n3 := podNetworkPoolName("default", "pn-a", "v4", "10.1.0.0/16")
	n4 := podNetworkPoolName("other", "pn-a", "v4", "10.0.0.0/16")
	if n1 != n2 {
		t.Errorf("expected stable name, got %q and %q", n1, n2)
	}
	if n1 == n3 {
		t.Errorf("expected distinct names for distinct CIDRs")
	}
	if n1 == n4 {
		t.Errorf("expected namespace to disambiguate cluster-scoped pool names")
	}
	if !strings.HasPrefix(n1, "pn-pn-a-v4-") {
		t.Errorf("unexpected name %q", n1)
	}
	if strings.ContainsAny(n1, "./:") {
		t.Errorf("name %q contains invalid characters", n1)
	}
}

func TestPoolBlockSize(t *testing.T) {
	// Large pool -> default block size.
	if bs := poolBlockSize("10.0.0.0/16", defaultIPv4BlockSize); bs != defaultIPv4BlockSize {
		t.Errorf("expected %d, got %d", defaultIPv4BlockSize, bs)
	}
	// Pool smaller than a default block -> pool prefix.
	if bs := poolBlockSize("10.0.0.0/28", defaultIPv4BlockSize); bs != 28 {
		t.Errorf("expected 28, got %d", bs)
	}
	// IPv6 large pool -> default.
	if bs := poolBlockSize("fd00::/48", defaultIPv6BlockSize); bs != defaultIPv6BlockSize {
		t.Errorf("expected %d, got %d", defaultIPv6BlockSize, bs)
	}
	// Invalid CIDR -> default fallback.
	if bs := poolBlockSize("not-a-cidr", defaultIPv4BlockSize); bs != defaultIPv4BlockSize {
		t.Errorf("expected default fallback %d, got %d", defaultIPv4BlockSize, bs)
	}
}

func TestPodNetworkReconciler_MapNetworkToPodNetworks(t *testing.T) {
	pnMatch := &nc.PodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "pn-match", Namespace: "default"},
		Spec:       nc.PodNetworkSpec{NetworkRef: "net-x"},
	}
	pnOther := &nc.PodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "pn-other", Namespace: "default"},
		Spec:       nc.PodNetworkSpec{NetworkRef: "net-y"},
	}
	network := &nc.Network{ObjectMeta: metav1.ObjectMeta{Name: "net-x", Namespace: "default"}}

	r := newPodNetworkReconciler(pnMatch, pnOther, network)

	reqs := r.mapNetworkToPodNetworks(context.Background(), network)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Name != "pn-match" {
		t.Errorf("expected pn-match, got %s", reqs[0].Name)
	}
}
