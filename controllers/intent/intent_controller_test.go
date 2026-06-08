package intent

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	intentreconciler "github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent"
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
