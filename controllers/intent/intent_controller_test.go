package intent

import (
	"context"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	intentreconciler "github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent"
)

const (
	testIntentNamespace = "default"
	testIntentScopeKey  = "scope"
	testIntentScope     = "edge"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(nc.AddToScheme(s))
	utilruntime.Must(networkv1alpha1.AddToScheme(s))
	return s
}

func newTestController(t *testing.T) *Controller {
	t.Helper()
	s := testScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()
	logger := zap.New(zap.UseDevMode(true))

	reconciler, err := intentreconciler.NewReconciler(fakeClient, logger, 60*time.Second, "")
	if err != nil {
		t.Fatalf("failed to create intent reconciler: %v", err)
	}

	return &Controller{
		Client:     fakeClient,
		Scheme:     s,
		Reconciler: reconciler,
	}
}

func newTestIntentReconciler(t *testing.T, objects ...client.Object) (client.Client, *intentreconciler.Reconciler) {
	t.Helper()
	s := testScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(
			&nc.VRF{},
			&nc.Network{},
			&nc.Destination{},
			&nc.Layer2Attachment{},
			&nc.Inbound{},
			&nc.Outbound{},
			&nc.PodNetwork{},
			&nc.BGPPeering{},
			&nc.Collector{},
			&nc.TrafficMirror{},
			&nc.AnnouncementPolicy{},
			&nc.NodeAttachment{},
		).
		WithObjects(objects...).
		Build()
	logger := zap.New(zap.UseDevMode(true))

	reconciler, err := intentreconciler.NewReconciler(fakeClient, logger, 60*time.Second, "")
	if err != nil {
		t.Fatalf("failed to create intent reconciler: %v", err)
	}

	return fakeClient, reconciler
}

func TestIntentReconcile_DoesNotRequeue(t *testing.T) {
	r := newTestController(t)
	result, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got RequeueAfter %v", result.RequeueAfter)
	}
}

func TestIntentReconcile_NoErrorOnEmptyCluster(t *testing.T) {
	r := newTestController(t)
	result, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatalf("expected no error on empty cluster, got %v", err)
	}
	if result.Requeue {
		t.Error("expected Requeue to be false")
	}
}

func TestIntentReconciler_ReconcilesFinalizersThroughWiring(t *testing.T) {
	ctx := context.Background()
	vrfRef := "vrf-in-use"
	vni := int32(100)
	routeTarget := "65188:100"
	vlan := int32(100)
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{Name: vrfRef, Namespace: testIntentNamespace},
		Spec: nc.VRFSpec{
			VRF:         "backbone",
			VNI:         &vni,
			RouteTarget: &routeTarget,
		},
	}
	network := &nc.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "network-in-use", Namespace: testIntentNamespace},
		Spec:       nc.NetworkSpec{VLAN: &vlan},
	}
	destination := &nc.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "destination-in-use",
			Namespace: testIntentNamespace,
			Labels:    map[string]string{testIntentScopeKey: testIntentScope},
		},
		Spec: nc.DestinationSpec{VRFRef: &vrfRef},
	}
	layer2 := &nc.Layer2Attachment{
		ObjectMeta: metav1.ObjectMeta{Name: "layer2-in-use", Namespace: testIntentNamespace},
		Spec: nc.Layer2AttachmentSpec{
			NetworkRef:   network.Name,
			Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{testIntentScopeKey: testIntentScope}},
		},
	}

	fakeClient, reconciler := newTestIntentReconciler(t, vrf, network, destination, layer2)
	if err := reconciler.ReconcileDebounced(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	assertHasFinalizer := func(t *testing.T, obj client.Object, finalizer string) {
		t.Helper()
		if !controllerutil.ContainsFinalizer(obj, finalizer) {
			t.Fatalf("finalizers for %s/%s = %v, want %q", obj.GetNamespace(), obj.GetName(), obj.GetFinalizers(), finalizer)
		}
	}

	gotVRF := &nc.VRF{}
	if err := fakeClient.Get(ctx, client.ObjectKeyFromObject(vrf), gotVRF); err != nil {
		t.Fatalf("get VRF: %v", err)
	}
	assertHasFinalizer(t, gotVRF, nc.FinalizerVRFInUse)

	gotNetwork := &nc.Network{}
	if err := fakeClient.Get(ctx, client.ObjectKeyFromObject(network), gotNetwork); err != nil {
		t.Fatalf("get Network: %v", err)
	}
	assertHasFinalizer(t, gotNetwork, nc.FinalizerNetworkInUse)

	gotDestination := &nc.Destination{}
	if err := fakeClient.Get(ctx, client.ObjectKeyFromObject(destination), gotDestination); err != nil {
		t.Fatalf("get Destination: %v", err)
	}
	assertHasFinalizer(t, gotDestination, nc.FinalizerDestinationInUse)

	if gotVRF.Status.ObservedGeneration != gotVRF.Generation {
		t.Fatalf("VRF status observedGeneration = %d, want %d", gotVRF.Status.ObservedGeneration, gotVRF.Generation)
	}

	if err := fakeClient.Get(ctx, client.ObjectKeyFromObject(destination), destination); err != nil {
		t.Fatalf("get Destination for detach: %v", err)
	}
	destination.Spec.VRFRef = nil
	nextHop := "192.0.2.1"
	destination.Spec.NextHop = &nc.NextHopConfig{IPv4: &nextHop}
	destination.Labels = map[string]string{testIntentScopeKey: "other"}
	if err := fakeClient.Update(ctx, destination); err != nil {
		t.Fatalf("detach Destination from VRF: %v", err)
	}
	if err := fakeClient.Delete(ctx, layer2); err != nil {
		t.Fatalf("delete Layer2Attachment: %v", err)
	}

	if err := reconciler.ReconcileDebounced(ctx); err != nil {
		t.Fatalf("reconcile after detaching references failed: %v", err)
	}

	for _, tc := range []struct {
		name      string
		obj       client.Object
		finalizer string
	}{
		{name: "VRF", obj: gotVRF, finalizer: nc.FinalizerVRFInUse},
		{name: "Network", obj: gotNetwork, finalizer: nc.FinalizerNetworkInUse},
		{name: "Destination", obj: gotDestination, finalizer: nc.FinalizerDestinationInUse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := fakeClient.Get(ctx, client.ObjectKeyFromObject(tc.obj), tc.obj); err != nil {
				t.Fatalf("get %s: %v", tc.name, err)
			}
			if controllerutil.ContainsFinalizer(tc.obj, tc.finalizer) {
				t.Fatalf("%s finalizers = %v, did not remove %q", tc.name, tc.obj.GetFinalizers(), tc.finalizer)
			}
		})
	}
}

func TestIntentReconciler_UpdatesStatusThroughWiring(t *testing.T) {
	ctx := context.Background()
	inbound := &nc.Inbound{
		ObjectMeta: metav1.ObjectMeta{Name: "unresolved-inbound", Namespace: testIntentNamespace},
		Spec: nc.InboundSpec{
			NetworkRef: "missing-network",
			Addresses:  &nc.AddressAllocation{IPv4: []string{"192.0.2.10/32"}},
		},
	}

	fakeClient, reconciler := newTestIntentReconciler(t, inbound)
	if err := reconciler.ReconcileDebounced(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	got := &nc.Inbound{}
	if err := fakeClient.Get(ctx, client.ObjectKeyFromObject(inbound), got); err != nil {
		t.Fatalf("get Inbound: %v", err)
	}

	resolved := apimeta.FindStatusCondition(got.Status.Conditions, nc.ConditionTypeResolved)
	if resolved == nil {
		t.Fatal("Inbound status has no Resolved condition")
	}
	if resolved.Status != metav1.ConditionFalse || resolved.Reason != "NetworkNotFound" {
		t.Fatalf("Resolved condition = %#v, want False/NetworkNotFound", resolved)
	}

	ready := apimeta.FindStatusCondition(got.Status.Conditions, nc.ConditionTypeReady)
	if ready == nil {
		t.Fatal("Inbound status has no Ready condition")
	}
	if ready.Status != metav1.ConditionFalse || ready.Reason != "NetworkNotFound" {
		t.Fatalf("Ready condition = %#v, want False/NetworkNotFound", ready)
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Fatalf("status observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
	}
}
