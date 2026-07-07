package sync

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

// newFakeSyncControllerAllStatus builds a controller whose management client
// registers every intent type as a status subresource, so status sync-back can
// be exercised for any CRD (not just Inbound/Outbound).
func newFakeSyncControllerAllStatus(mgmtObjs, remoteObjs []client.Object) (*Controller, client.Client) {
	s := testScheme()

	mgmtClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(mgmtObjs...).
		WithStatusSubresource(
			&nc.VRF{}, &nc.Network{}, &nc.Destination{}, &nc.Layer2Attachment{},
			&nc.Inbound{}, &nc.Outbound{}, &nc.PodNetwork{}, &nc.BGPPeering{},
			&nc.Collector{}, &nc.TrafficMirror{}, &nc.AnnouncementPolicy{},
			&nc.NodeAttachment{}, &nc.InterfaceConfig{},
		).
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

// readyCondition is a small helper for constructing test conditions.
func readyCondition(status metav1.ConditionStatus, gen int64) metav1.Condition {
	return metav1.Condition{
		Type:               nc.ConditionTypeReady,
		Status:             status,
		Reason:             "Ready",
		Message:            "VRF is ready",
		ObservedGeneration: gen,
	}
}

// TestDesiredMirroredConditionsCaughtUp verifies the allowlist and the
// observedGeneration rewrite: only Ready/Resolved survive, off-allowlist types
// are dropped, and each mirrored condition carries the management generation.
func TestDesiredMirroredConditionsCaughtUp(t *testing.T) {
	remote := []metav1.Condition{
		{Type: nc.ConditionTypeReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 99},
		{Type: nc.ConditionTypeResolved, Status: metav1.ConditionTrue, Reason: "AllResolved", ObservedGeneration: 99},
		{Type: nc.ConditionTypeApplied, Status: metav1.ConditionTrue, Reason: "Applied", ObservedGeneration: 99},
		{Type: nc.ConditionTypeInterfaceNotFound, Status: metav1.ConditionFalse, Reason: "x", ObservedGeneration: 99},
	}

	got := desiredMirroredConditions(remote, true, 7)

	if len(got) != 2 {
		t.Fatalf("expected 2 mirrored conditions (Ready, Resolved), got %d: %+v", len(got), got)
	}
	for _, c := range got {
		if c.Type != nc.ConditionTypeReady && c.Type != nc.ConditionTypeResolved {
			t.Errorf("off-allowlist condition %q was mirrored", c.Type)
		}
		if c.ObservedGeneration != 7 {
			t.Errorf("condition %q observedGeneration = %d, want mgmt gen 7", c.Type, c.ObservedGeneration)
		}
	}
}

// TestDesiredMirroredConditionsProgressing verifies that a workload that has not
// caught up yields non-authoritative Ready=False/Progressing and
// Resolved=Unknown/Progressing conditions, never a stale Ready=True or
// Resolved=True from the workload.
func TestDesiredMirroredConditionsProgressing(t *testing.T) {
	remote := []metav1.Condition{
		{Type: nc.ConditionTypeReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 3},
		{Type: nc.ConditionTypeResolved, Status: metav1.ConditionTrue, Reason: "AllResolved", ObservedGeneration: 3},
	}

	got := desiredMirroredConditions(remote, false, 5)

	if len(got) != 2 {
		t.Fatalf("expected Ready+Resolved Progressing conditions, got %d: %+v", len(got), got)
	}
	ready := findCondition(got, nc.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "Progressing" {
		t.Errorf("expected Ready=False/Progressing, got %+v", ready)
	}
	resolved := findCondition(got, nc.ConditionTypeResolved)
	if resolved == nil || resolved.Status != metav1.ConditionUnknown || resolved.Reason != "Progressing" {
		t.Errorf("expected Resolved=Unknown/Progressing, got %+v", resolved)
	}
	for _, c := range got {
		if c.ObservedGeneration != 5 {
			t.Errorf("condition %q observedGeneration = %d, want mgmt gen 5", c.Type, c.ObservedGeneration)
		}
	}
}

// TestWorkloadCaughtUp exercises the two gates: the source-generation annotation
// must match the current management generation, and the workload must have
// observed its own current spec.
func TestWorkloadCaughtUp(t *testing.T) {
	makeRemote := func(sourceGen string, gen, observed int64) *nc.VRF {
		return &nc.VRF{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "vrf",
				Generation:  gen,
				Annotations: map[string]string{annotationSourceGeneration: sourceGen},
			},
			Status: nc.VRFStatus{ObservedGeneration: observed},
		}
	}

	tests := []struct {
		name    string
		remote  *nc.VRF
		mgmtGen int64
		want    bool
	}{
		{"caught up", makeRemote("4", 2, 2), 4, true},
		{"annotation stale", makeRemote("3", 2, 2), 4, false},
		{"workload lagging", makeRemote("4", 2, 1), 4, false},
		{"annotation missing", &nc.VRF{ObjectMeta: metav1.ObjectMeta{Name: "vrf"}}, 4, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := workloadCaughtUp(tc.remote, tc.mgmtGen); got != tc.want {
				t.Errorf("workloadCaughtUp = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStatusConditionsPtr verifies the reflection helper returns a live pointer
// for intent CRDs and nil for types without a status.conditions field.
func TestStatusConditionsPtr(t *testing.T) {
	vrf := &nc.VRF{}
	ptr := statusConditionsPtr(vrf)
	if ptr == nil {
		t.Fatal("expected non-nil conditions pointer for VRF")
	}
	*ptr = append(*ptr, readyCondition(metav1.ConditionTrue, 1))
	if len(vrf.Status.Conditions) != 1 {
		t.Errorf("pointer is not live: VRF conditions = %d", len(vrf.Status.Conditions))
	}

	if statusConditionsPtr(&corev1.Secret{}) != nil {
		t.Error("expected nil conditions pointer for Secret")
	}
}

// TestStatusObservedGeneration verifies the reflection reader.
func TestStatusObservedGeneration(t *testing.T) {
	vrf := &nc.VRF{Status: nc.VRFStatus{ObservedGeneration: 12}}
	got, ok := statusObservedGeneration(vrf)
	if !ok || got != 12 {
		t.Errorf("statusObservedGeneration(VRF) = %d, %v; want 12, true", got, ok)
	}

	if _, ok := statusObservedGeneration(&corev1.Secret{}); ok {
		t.Error("expected ok=false for Secret (no observedGeneration field)")
	}
}

// TestReconcileMirrorsWorkloadReadyBack is the happy-path integration test: a
// workload VRF that is Ready and caught up has its Ready condition mirrored onto
// the management VRF after Reconcile.
func TestReconcileMirrorsWorkloadReadyBack(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{Name: "vrf-m2m", Namespace: "test-cluster"},
		Spec:       nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}
	// Workload copy: managed by us, already reconciled and Ready. source-generation
	// is stamped by the forward pass during Reconcile, so it does not need to be
	// pre-set here; observedGeneration==generation makes it caught up.
	remote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-m2m",
			Namespace: testRemoteNamespace,
			Labels:    map[string]string{labelManagedBy: labelManagedByValue},
		},
		Spec: nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
		Status: nc.VRFStatus{
			ObservedGeneration: 0,
			Conditions:         []metav1.Condition{readyCondition(metav1.ConditionTrue, 0)},
		},
	}

	sc, _ := newFakeSyncControllerAllStatus([]client.Object{vrf}, []client.Object{remote})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := sc.Client.Get(ctx, types.NamespacedName{Namespace: "test-cluster", Name: "vrf-m2m"}, got); err != nil {
		t.Fatalf("Get mgmt VRF: %v", err)
	}
	cond := findCondition(got.Status.Conditions, nc.ConditionTypeReady)
	if cond == nil {
		t.Fatalf("Ready condition was not mirrored back onto mgmt VRF: %+v", got.Status.Conditions)
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("mirrored Ready = %q, want True", cond.Status)
	}
	if cond.ObservedGeneration != got.Generation {
		t.Errorf("mirrored observedGeneration = %d, want mgmt generation %d", cond.ObservedGeneration, got.Generation)
	}
}

// TestReconcileStatusBackDoesNotWipeAddresses is the brick regression: an empty
// workload status must never overwrite the management-owned status.addresses
// (which feed the workload spec via IPAM promotion). Addresses and spec must
// survive a status sync-back.
func TestReconcileStatusBackDoesNotWipeAddresses(t *testing.T) {
	count := int32(2)
	inbound := &nc.Inbound{
		ObjectMeta: metav1.ObjectMeta{Name: "ib-test", Namespace: "test-cluster"},
		Spec: nc.InboundSpec{
			NetworkRef:    "net-vlan501",
			Count:         &count,
			Advertisement: nc.AdvertisementConfig{Type: "bgp"},
		},
		Status: nc.InboundStatus{
			Addresses: &nc.AddressAllocation{IPv4: []string{"10.250.0.2", "10.250.0.3"}},
		},
	}
	// Workload copy with an EMPTY status (no addresses, no conditions).
	remote := &nc.Inbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ib-test",
			Namespace: testRemoteNamespace,
			Labels:    map[string]string{labelManagedBy: labelManagedByValue},
		},
		Spec: nc.InboundSpec{NetworkRef: "net-vlan501", Advertisement: nc.AdvertisementConfig{Type: "bgp"}},
	}

	sc, _ := newFakeSyncControllerAllStatus([]client.Object{inbound}, []client.Object{remote})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.Inbound{}
	if err := sc.Client.Get(ctx, types.NamespacedName{Namespace: "test-cluster", Name: "ib-test"}, got); err != nil {
		t.Fatalf("Get mgmt Inbound: %v", err)
	}
	if got.Status.Addresses == nil || len(got.Status.Addresses.IPv4) != 2 {
		t.Fatalf("mgmt status.addresses was clobbered by status sync-back: %+v", got.Status.Addresses)
	}
	if got.Spec.NetworkRef != "net-vlan501" {
		t.Errorf("mgmt spec was mutated: %+v", got.Spec)
	}
}

// TestReconcileBlocksNamespaceWithMultipleRemotes verifies that a namespace
// mapping to more than one workload cluster is refused entirely: no forward sync
// happens, every intent resource is marked Ready=False/MultipleWorkloadClusters,
// and a Warning event is emitted.
func TestReconcileBlocksNamespaceWithMultipleRemotes(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{Name: "vrf-m2m", Namespace: "test-cluster"},
		Spec:       nc.VRFSpec{VRF: "m2m", VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	// Two empty remotes in the same namespace; neither must receive the VRF.
	remoteA := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	sc, _ := newFakeSyncControllerAllStatus([]client.Object{vrf}, nil)
	sc.Remotes.clients[types.NamespacedName{Namespace: "test-cluster", Name: "test-cluster"}] = remoteA
	sc.Remotes.clients[types.NamespacedName{Namespace: "test-cluster", Name: "second"}] =
		fake.NewClientBuilder().WithScheme(testScheme()).Build()
	recorder := record.NewFakeRecorder(10)
	sc.Recorder = recorder

	ctx := context.Background()
	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: "sync"},
	})
	// The error is returned so the controller's rate limiter applies exponential
	// backoff while the misconfiguration persists.
	if !errors.Is(err, errMultipleWorkloadClusters) {
		t.Fatalf("expected errMultipleWorkloadClusters, got %v", err)
	}

	// No forward sync: the VRF must not have been created on either remote.
	remoteVRF := &nc.VRF{}
	if err := remoteA.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-m2m"}, remoteVRF); err == nil {
		t.Error("VRF was forward-synced to a remote despite the namespace being blocked")
	}

	// The mgmt VRF must be marked not-ready with the block reason.
	got := &nc.VRF{}
	if err := sc.Client.Get(ctx, types.NamespacedName{Namespace: "test-cluster", Name: "vrf-m2m"}, got); err != nil {
		t.Fatalf("Get mgmt VRF: %v", err)
	}
	cond := findCondition(got.Status.Conditions, nc.ConditionTypeReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "MultipleWorkloadClusters" {
		t.Fatalf("expected Ready=False/MultipleWorkloadClusters, got %+v", got.Status.Conditions)
	}

	// A Warning event must have been emitted.
	select {
	case e := <-recorder.Events:
		if !strings.Contains(e, "MultipleWorkloadClusters") {
			t.Errorf("unexpected event %q", e)
		}
	default:
		t.Error("expected a Warning event for the multiple-workload-cluster block")
	}
}

func findCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}
