package sync

import (
	"context"
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

	remotes := NewRemoteClientManager(s)
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
}

// TestSyncNoRemoteClient verifies requeue when no remote client exists.
func TestSyncNoRemoteClient(t *testing.T) {
	s := testScheme()
	mgmtClient := fake.NewClientBuilder().WithScheme(s).Build()
	remotes := NewRemoteClientManager(s)

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
	m := NewRemoteClientManager(s)

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

// Ensure corev1 import is used (for scheme registration).
var _ = &corev1.Secret{}
