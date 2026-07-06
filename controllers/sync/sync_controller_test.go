package sync

import (
	"context"
	"net"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(nc.AddToScheme(s))
	return s
}

const testRemoteNamespace = "default"

func newFakeSyncController(mgmtObjs, remoteObjs []client.Object) (*Controller, client.Client) {
	s := testScheme()

	mgmtClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(mgmtObjs...).
		WithStatusSubresource(&nc.Inbound{}, &nc.Outbound{}).
		Build()

	remoteClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(remoteObjs...).
		Build()

	remotes := NewRemoteClientManager(s, RemoteClientConfig{})
	remotes.clients[types.NamespacedName{Namespace: "test-cluster", Name: "test-cluster"}] = remoteClient

	return &Controller{
		Client:  mgmtClient,
		Scheme:  s,
		Log:     zap.New(zap.UseDevMode(true)),
		Remotes: remotes,
	}, remoteClient
}

// TestSyncCreatesRemoteObject verifies that a VRF in the mgmt namespace
// gets created on the remote cluster.
func TestSyncCreatesRemoteObject(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: "test-cluster",
		},
		Spec: nc.VRFSpec{
			VRF:         "m2m",
			VNI:         ptrInt32(2002026),
			RouteTarget: ptrString("65188:2026"),
		},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, nil)
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Check remote cluster has the VRF.
	remoteVRF := &nc.VRF{}
	err = remoteClient.Get(ctx, types.NamespacedName{
		Namespace: testRemoteNamespace,
		Name:      "vrf-m2m",
	}, remoteVRF)
	if err != nil {
		t.Fatalf("Remote VRF not found: %v", err)
	}

	if remoteVRF.Spec.VRF != "m2m" {
		t.Errorf("Expected VRF name 'm2m', got %q", remoteVRF.Spec.VRF)
	}
	if remoteVRF.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("Expected managed-by label, got %v", remoteVRF.Labels)
	}
	if remoteVRF.Annotations[annotationSourceNS] != "test-cluster" {
		t.Errorf("Expected source-namespace annotation, got %v", remoteVRF.Annotations)
	}
}

// TestSyncUpdatesRemoteObject verifies drift correction.
func TestSyncUpdatesRemoteObject(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: "test-cluster",
		},
		Spec: nc.VRFSpec{
			VRF:         "m2m",
			VNI:         ptrInt32(2002026),
			RouteTarget: ptrString("65188:2026"),
		},
	}

	// Remote has stale data.
	staleRemote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
			},
		},
		Spec: nc.VRFSpec{
			VRF:         "m2m",
			VNI:         ptrInt32(9999), // Drifted VNI
			RouteTarget: ptrString("65188:2026"),
		},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{staleRemote})
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	remoteVRF := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-m2m"}, remoteVRF); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}
	if remoteVRF.Spec.VNI == nil || *remoteVRF.Spec.VNI != 2002026 {
		t.Errorf("Expected VNI 2002026, got %v (drift not corrected)", remoteVRF.Spec.VNI)
	}
}

// TestSyncDeletion verifies that deleting a mgmt object removes it from remote.
func TestSyncDeletion(t *testing.T) {
	now := metav1.Now()
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "vrf-m2m",
			Namespace:         "test-cluster",
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	remoteVRF := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: testRemoteNamespace,
			Labels:    map[string]string{labelManagedBy: labelManagedByValue},
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remoteVRF})
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Remote object should be deleted.
	err = remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-m2m"}, &nc.VRF{})
	if err == nil {
		t.Error("Expected remote VRF to be deleted, but it still exists")
	}

	// Mgmt object should be gone (fake client GCs when last finalizer removed + DeletionTimestamp set).
	mgmtVRF := &nc.VRF{}
	err = sc.Client.Get(ctx, types.NamespacedName{Namespace: "test-cluster", Name: "vrf-m2m"}, mgmtVRF)
	if err == nil {
		// If it still exists, check that finalizer was removed.
		for _, f := range mgmtVRF.Finalizers {
			if f == finalizerName {
				t.Error("Finalizer should have been removed after remote deletion")
			}
		}
	}
	// err != nil (not found) is the expected case — object was GC'd.
}

// TestSyncIPAMPromotion verifies that status.addresses on Inbound gets
// promoted to spec.addresses on the remote object.
func TestSyncIPAMPromotion(t *testing.T) {
	count := int32(2)
	inbound := &nc.Inbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ib-test",
			Namespace: "test-cluster",
		},
		Spec: nc.InboundSpec{
			NetworkRef:    "net-vlan501",
			Count:         &count,
			Advertisement: nc.AdvertisementConfig{Type: "bgp"},
		},
		Status: nc.InboundStatus{
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.250.0.2", "10.250.0.3"},
				IPv6: []string{"fd94:685b:30cf:501::2", "fd94:685b:30cf:501::3"},
			},
		},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{inbound}, nil)
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	remoteInbound := &nc.Inbound{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "ib-test"}, remoteInbound); err != nil {
		t.Fatalf("Remote Inbound not found: %v", err)
	}

	if remoteInbound.Spec.Addresses == nil {
		t.Fatal("Remote Inbound spec.addresses should be promoted from status")
	}
	if len(remoteInbound.Spec.Addresses.IPv4) != 2 {
		t.Errorf("Expected 2 IPv4 addresses, got %d", len(remoteInbound.Spec.Addresses.IPv4))
	}
	if remoteInbound.Spec.Count != nil {
		t.Error("Remote Inbound spec.count should be nil after IPAM promotion")
	}

	// IPAM stores bare host IPs in status; spec.addresses is a CIDR-typed field
	// validated by the vinbound webhook (net.ParseCIDR). The promoted addresses
	// must be valid CIDRs or the remote create is rejected. This is the exact
	// production failure: "invalid IPv4 CIDR \"10.100.148.1\"".
	for _, addr := range remoteInbound.Spec.Addresses.IPv4 {
		if _, _, err := net.ParseCIDR(addr); err != nil {
			t.Errorf("promoted IPv4 address %q is not a valid CIDR: %v", addr, err)
		}
	}
	for _, addr := range remoteInbound.Spec.Addresses.IPv6 {
		if _, _, err := net.ParseCIDR(addr); err != nil {
			t.Errorf("promoted IPv6 address %q is not a valid CIDR: %v", addr, err)
		}
	}

	// The promoted remote object must also pass the real admission webhook that
	// rejected it in production.
	if _, err := (&nc.Inbound{}).ValidateCreate(ctx, remoteInbound); err != nil {
		t.Errorf("promoted remote Inbound rejected by vinbound webhook: %v", err)
	}
}

// TestPromoteIPAMAddressesFormatsHostCIDR reproduces the production bug directly:
// a bare host IP allocated by IPAM (e.g. "10.100.148.1") must be promoted into
// spec.addresses as a host CIDR (/32 for IPv4, /128 for IPv6) so the vinbound
// webhook accepts it. Entries that already carry a prefix must be left intact.
func TestPromoteIPAMAddressesFormatsHostCIDR(t *testing.T) {
	inbound := &nc.Inbound{
		Spec: nc.InboundSpec{NetworkRef: "net"},
		Status: nc.InboundStatus{
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.100.148.1", "10.100.148.0/24"},
				IPv6: []string{"fd00::1"},
			},
		},
	}

	(&Controller{}).promoteIPAMAddresses(inbound)

	if inbound.Spec.Addresses == nil {
		t.Fatal("spec.addresses should be populated from status")
	}
	wantV4 := []string{"10.100.148.1/32", "10.100.148.0/24"}
	if len(inbound.Spec.Addresses.IPv4) != len(wantV4) {
		t.Fatalf("expected %d IPv4 entries, got %v", len(wantV4), inbound.Spec.Addresses.IPv4)
	}
	for i, want := range wantV4 {
		if inbound.Spec.Addresses.IPv4[i] != want {
			t.Errorf("IPv4[%d] = %q, want %q", i, inbound.Spec.Addresses.IPv4[i], want)
		}
	}
	if len(inbound.Spec.Addresses.IPv6) != 1 || inbound.Spec.Addresses.IPv6[0] != "fd00::1/128" {
		t.Errorf("IPv6 = %v, want [fd00::1/128]", inbound.Spec.Addresses.IPv6)
	}

	// The whole point: the promoted spec now passes admission validation.
	if _, err := (&nc.Inbound{}).ValidateCreate(context.Background(), inbound); err != nil {
		t.Errorf("promoted Inbound rejected by vinbound webhook: %v", err)
	}
}

// TestSyncNoRemoteClient verifies requeue when no remote client exists.
func TestSyncNoRemoteClient(t *testing.T) {
	s := testScheme()
	mgmtClient := fake.NewClientBuilder().WithScheme(s).Build()
	remotes := NewRemoteClientManager(s, RemoteClientConfig{})

	sc := &Controller{
		Client:  mgmtClient,
		Scheme:  s,
		Log:     zap.New(zap.UseDevMode(true)),
		Remotes: remotes,
	}

	result, err := sc.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "unknown", Name: "sync"},
	})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("Expected requeue when no remote client")
	}
}

// TestSyncDrainsFinalizerWhenRemoteGone verifies that when no remote client
// exists for the namespace (workload cluster deleted), an intent CR being
// deleted has our finalizer removed so it can complete deletion. Without this,
// deleting a CAPI Cluster wedges every intent CR in Terminating forever.
func TestSyncDrainsFinalizerWhenRemoteGone(t *testing.T) {
	now := metav1.Now()
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "vrf-stuck",
			Namespace:         "orphaned-cluster",
			Finalizers:        []string{finalizerName},
			DeletionTimestamp: &now,
		},
		Spec: nc.VRFSpec{VRF: "stuck", VNI: ptrInt32(2002099), RouteTarget: ptrString("65188:99")},
	}

	s := testScheme()
	mgmtClient := fake.NewClientBuilder().WithScheme(s).WithObjects(vrf).Build()
	remotes := NewRemoteClientManager(s, RemoteClientConfig{})

	sc := &Controller{
		Client:  mgmtClient,
		Scheme:  s,
		Log:     zap.New(zap.UseDevMode(true)),
		Remotes: remotes,
	}

	if _, err := sc.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "orphaned-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// VRF should now be gone (fake client GCs once last finalizer is removed).
	got := &nc.VRF{}
	err := mgmtClient.Get(context.Background(), types.NamespacedName{Namespace: "orphaned-cluster", Name: "vrf-stuck"}, got)
	if err == nil {
		if len(got.Finalizers) != 0 {
			t.Errorf("Expected finalizer to be drained, still present: %v", got.Finalizers)
		}
	}
}

// TestSyncNoRemoteClientLeavesActiveCRsAlone verifies that when no remote
// client exists, intent CRs that are NOT being deleted are left untouched
// (no finalizer added, no error).
func TestSyncNoRemoteClientLeavesActiveCRsAlone(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-alive",
			Namespace: "pending-cluster",
		},
		Spec: nc.VRFSpec{VRF: "alive", VNI: ptrInt32(2002098), RouteTarget: ptrString("65188:98")},
	}

	s := testScheme()
	mgmtClient := fake.NewClientBuilder().WithScheme(s).WithObjects(vrf).Build()
	remotes := NewRemoteClientManager(s, RemoteClientConfig{})

	sc := &Controller{
		Client:  mgmtClient,
		Scheme:  s,
		Log:     zap.New(zap.UseDevMode(true)),
		Remotes: remotes,
	}

	if _, err := sc.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "pending-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	got := &nc.VRF{}
	if err := mgmtClient.Get(context.Background(), types.NamespacedName{Namespace: "pending-cluster", Name: "vrf-alive"}, got); err != nil {
		t.Fatalf("VRF should still exist: %v", err)
	}
	if len(got.Finalizers) != 0 {
		t.Errorf("Expected no finalizer added without remote client, got: %v", got.Finalizers)
	}
}

// TestSyncRefusesUnmanagedObject verifies we don't overwrite objects we don't own.
func TestSyncRefusesUnmanagedObject(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: "test-cluster",
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	// Remote object exists WITHOUT our managed-by label.
	unmanagedRemote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: testRemoteNamespace,
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(1), RouteTarget: ptrString("1:1")},
	}

	sc, _ := newFakeSyncController([]client.Object{vrf}, []client.Object{unmanagedRemote})
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	})
	if err == nil {
		t.Fatal("Expected error when remote object is not managed by us")
	}
}

// TestRemoteClientManager tests basic CRUD on the client manager.
func TestRemoteClientManager(t *testing.T) {
	s := testScheme()
	m := NewRemoteClientManager(s, RemoteClientConfig{})

	if m.Has(types.NamespacedName{Namespace: "ns1", Name: "c1"}) {
		t.Error("Should not have ns1/c1 initially")
	}
	if m.Get(types.NamespacedName{Namespace: "ns1", Name: "c1"}) != nil {
		t.Error("Get should return nil for unknown cluster")
	}

	// We can't test UpdateFromKubeconfig without a real cluster,
	// but we can test Has/Get/Remove with direct injection.
	m.clients[types.NamespacedName{Namespace: "ns1", Name: "c1"}] = fake.NewClientBuilder().WithScheme(s).Build()

	if !m.Has(types.NamespacedName{Namespace: "ns1", Name: "c1"}) {
		t.Error("Should have ns1/c1 after injection")
	}
	if m.Get(types.NamespacedName{Namespace: "ns1", Name: "c1"}) == nil {
		t.Error("Get should return client for ns1/c1")
	}

	m.Remove(types.NamespacedName{Namespace: "ns1", Name: "c1"})
	if m.Has(types.NamespacedName{Namespace: "ns1", Name: "c1"}) {
		t.Error("Should not have ns1/c1 after removal")
	}
}

// TestSyncMultipleCRDTypes verifies that multiple CRD types are synced in one reconcile.
func TestSyncMultipleCRDTypes(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{Name: "vrf-m2m", Namespace: "test-cluster"},
		Spec:       nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}
	network := &nc.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "net-vlan501", Namespace: "test-cluster"},
		Spec:       nc.NetworkSpec{VLAN: ptrInt32(501)},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf, network}, nil)
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Both should exist on remote.
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-m2m"}, &nc.VRF{}); err != nil {
		t.Errorf("Remote VRF not found: %v", err)
	}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "net-vlan501"}, &nc.Network{}); err != nil {
		t.Errorf("Remote Network not found: %v", err)
	}
}

func ptrInt32(v int32) *int32    { return &v }
func ptrString(v string) *string { return &v }

// TestSyncPreservesForeignLabelsOnRemote verifies that non-GitOps labels set by
// another controller on the workload cluster are preserved, while a Flux/GitOps
// inventory label is stripped even when it was already present on the remote.
// The patch helper only diffs the fields we change, so the foreign label is
// never sent; the Flux label is actively removed because our synced objects are
// not part of any Flux inventory (if a live Flux truly owns it, it reapplies).
func TestSyncPreservesForeignLabelsOnRemote(t *testing.T) {
	const (
		fluxLabel    = "kustomize.toolkit.fluxcd.io/name" // must be stripped
		foreignLabel = "example.com/owned-by"             // must be preserved
	)

	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{Name: "vrf-m2m", Namespace: "test-cluster"},
		Spec:       nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	// Remote object is ours (managed-by label) but a workload-cluster Flux has
	// also stamped its own inventory label on it, another controller stamped a
	// non-GitOps label, and the spec has drifted.
	remote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
				fluxLabel:      "workload-apps",
				foreignLabel:   "some-operator",
			},
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(9999), RouteTarget: ptrString("65188:2026")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remote})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-m2m"}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}

	// Spec drift corrected.
	if got.Spec.VNI == nil || *got.Spec.VNI != 2002026 {
		t.Errorf("Expected VNI 2002026 (drift corrected), got %v", got.Spec.VNI)
	}
	// Our managed-by label still present.
	if got.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("managed-by label lost, got %v", got.Labels)
	}
	// The foreign non-GitOps label must survive untouched.
	if got.Labels[foreignLabel] != "some-operator" {
		t.Errorf("foreign non-GitOps label clobbered: got %v", got.Labels)
	}
	// The Flux inventory label must be stripped from the remote object.
	if _, ok := got.Labels[fluxLabel]; ok {
		t.Errorf("Flux inventory label was not stripped from remote: %v", got.Labels)
	}
}

// TestSyncDoesNotPropagateFluxLabels verifies that GitOps inventory labels present
// on the management-cluster source object are stripped and never land on the
// remote copy, so a workload-cluster Flux does not adopt/prune our synced objects.
func TestSyncDoesNotPropagateFluxLabels(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: "test-cluster",
			Labels: map[string]string{
				"kustomize.toolkit.fluxcd.io/name":      "mgmt-intents",
				"kustomize.toolkit.fluxcd.io/namespace": "flux-system",
				"app.kubernetes.io/part-of":             "network", // legitimate, must propagate
			},
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, nil)
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-m2m"}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}

	for k := range got.Labels {
		if k == "kustomize.toolkit.fluxcd.io/name" || k == "kustomize.toolkit.fluxcd.io/namespace" {
			t.Errorf("Flux inventory label %q was propagated to remote: %v", k, got.Labels)
		}
	}
	if got.Labels["app.kubernetes.io/part-of"] != "network" {
		t.Errorf("legitimate source label was not propagated, got %v", got.Labels)
	}
	if got.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("managed-by label missing, got %v", got.Labels)
	}
}

// TestSyncBGPSecretsMirrorsReferencedSecret verifies that a Secret referenced
// by a BGPPeering.spec.authSecretRef is copied into the remote namespace,
// stamped with our managed-by label, and contains the same Data.
func TestSyncBGPSecretsMirrorsReferencedSecret(t *testing.T) {
	bp := &nc.BGPPeering{
		ObjectMeta: metav1.ObjectMeta{Name: "lp", Namespace: "test-cluster"},
		Spec: nc.BGPPeeringSpec{
			Mode:          nc.BGPPeeringModeLoopbackPeer,
			Ref:           nc.BGPPeeringRef{InboundRefs: []string{"x"}},
			AuthSecretRef: &corev1.LocalObjectReference{Name: "bgp-auth"},
		},
	}
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bgp-auth", Namespace: "test-cluster"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"password": []byte("s3cret")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{bp, src}, nil)
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &corev1.Secret{}
	if err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: testRemoteNamespace, Name: "bgp-auth",
	}, got); err != nil {
		t.Fatalf("Remote Secret not found: %v", err)
	}
	if string(got.Data["password"]) != "s3cret" {
		t.Errorf("Expected password 's3cret', got %q", string(got.Data["password"]))
	}
	if got.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("Expected managed-by label, got %v", got.Labels)
	}
	if got.Annotations[annotationSourceNS] != "test-cluster" {
		t.Errorf("Expected source-namespace annotation, got %v", got.Annotations)
	}
}

// TestSyncBGPSecretsSweepsOrphan verifies that a previously-synced Secret
// (managed-by label + source-namespace annotation) is removed from the
// remote namespace once no live BGPPeering references it any more.
func TestSyncBGPSecretsSweepsOrphan(t *testing.T) {
	orphan := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stale-auth",
			Namespace: testRemoteNamespace,
			Labels:    map[string]string{labelManagedBy: labelManagedByValue},
			Annotations: map[string]string{
				annotationSourceNS: "test-cluster",
			},
		},
		Data: map[string][]byte{"password": []byte("old")},
	}

	sc, remoteClient := newFakeSyncController(nil, []client.Object{orphan})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &corev1.Secret{}
	err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: testRemoteNamespace, Name: "stale-auth",
	}, got)
	if err == nil {
		t.Fatalf("Expected orphan Secret to be deleted, but it still exists")
	}
}

// Ensure corev1 import is used (for scheme registration).
var _ = &corev1.Secret{}

// TestSyncPrunesRemovedSourceLabel is the convergence counterpart to the
// foreign-label test. A label we propagated on an earlier sync (recorded in the
// managed-labels tracking annotation) but that has since been removed from the
// source must be pruned from the remote object, while a foreign label we never
// managed is left untouched.
func TestSyncPrunesRemovedSourceLabel(t *testing.T) {
	const (
		foreignLabel = "example.com/owned-by"      // foreign, non-GitOps: must survive
		droppedLabel = "team"                      // we propagated this before, now gone
		keptLabel    = "app.kubernetes.io/part-of" // still on the source
	)

	// Source no longer carries droppedLabel.
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: "test-cluster",
			Labels:    map[string]string{keptLabel: "network"},
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	// Remote reflects a previous sync: we managed part-of, managed-by and team,
	// and another controller independently stamped its own non-GitOps label.
	remote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
				keptLabel:      "network",
				droppedLabel:   "net", // stale: we set it last time, source dropped it
				foreignLabel:   "some-operator",
			},
			Annotations: map[string]string{
				annotationSourceNS: "test-cluster",
				annotationManagedLabels: strings.Join([]string{
					keptLabel, labelManagedBy, droppedLabel,
				}, ","),
				annotationManagedAnnotations: annotationSourceNS,
			},
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remote})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-m2m"}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}

	// The label we used to manage but dropped upstream must be gone.
	if _, ok := got.Labels[droppedLabel]; ok {
		t.Errorf("stale managed label %q was not pruned: %v", droppedLabel, got.Labels)
	}
	// The foreign non-GitOps label we never managed must survive.
	if got.Labels[foreignLabel] != "some-operator" {
		t.Errorf("foreign label %q was clobbered: %v", foreignLabel, got.Labels)
	}
	// The still-desired source label and our managed-by label must remain.
	if got.Labels[keptLabel] != "network" {
		t.Errorf("desired label %q missing: %v", keptLabel, got.Labels)
	}
	if got.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("managed-by label missing: %v", got.Labels)
	}
}

// TestSyncPrunesPreviouslyPropagatedFluxLabel covers the exact regression Max
// flagged: a Flux/GitOps label the sync controller itself propagated before it
// learned to strip them (so it is in our managed set) must be cleaned up on the
// next sync, because the freshly built desired object no longer carries it.
func TestSyncPrunesPreviouslyPropagatedFluxLabel(t *testing.T) {
	const propagatedFluxLabel = "kustomize.toolkit.fluxcd.io/namespace"

	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{Name: "vrf-m2m", Namespace: "test-cluster"},
		Spec:       nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	// Remote still carries a Flux label we propagated before, AND has it recorded
	// in our managed-labels tracking annotation — so we own it and must remove it.
	remote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy:      labelManagedByValue,
				propagatedFluxLabel: "flux-system",
			},
			Annotations: map[string]string{
				annotationSourceNS:           "test-cluster",
				annotationManagedLabels:      labelManagedBy + "," + propagatedFluxLabel,
				annotationManagedAnnotations: annotationSourceNS,
			},
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remote})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-m2m"}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}

	if _, ok := got.Labels[propagatedFluxLabel]; ok {
		t.Errorf("previously-propagated Flux label %q was not pruned: %v", propagatedFluxLabel, got.Labels)
	}
	if got.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("managed-by label missing: %v", got.Labels)
	}
}

// TestSyncRecordsManagedKeysOnCreate verifies that a freshly created remote
// object carries the tracking annotations enumerating the label and annotation
// keys we own, so the very next sync has the ownership information it needs to
// prune keys we later drop.
func TestSyncRecordsManagedKeysOnCreate(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: "test-cluster",
			Labels:    map[string]string{"app.kubernetes.io/part-of": "network"},
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, nil)
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-m2m"}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}

	wantLabelKeys := "app.kubernetes.io/part-of," + labelManagedBy
	if got.Annotations[annotationManagedLabels] != wantLabelKeys {
		t.Errorf("managed-labels tracking annotation = %q, want %q",
			got.Annotations[annotationManagedLabels], wantLabelKeys)
	}
	if got.Annotations[annotationManagedAnnotations] != annotationSourceNS {
		t.Errorf("managed-annotations tracking annotation = %q, want %q",
			got.Annotations[annotationManagedAnnotations], annotationSourceNS)
	}
}
