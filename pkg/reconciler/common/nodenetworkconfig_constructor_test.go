package common_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/common"
)

type noopConfigApplier struct{}

func (noopConfigApplier) ApplyConfig(_ context.Context, _ *v1alpha1.NodeNetworkConfig) error {
	return nil
}

type typedNilStubHealthChecker struct{}

func (*typedNilStubHealthChecker) CheckInterfaces() error                 { return nil }
func (*typedNilStubHealthChecker) CheckReachability() error               { return nil }
func (*typedNilStubHealthChecker) CheckAPIServer(_ context.Context) error { return nil }
func (*typedNilStubHealthChecker) TaintsRemoved() bool                    { return true }
func (*typedNilStubHealthChecker) RemoveTaints(_ context.Context) error   { return nil }
func (*typedNilStubHealthChecker) UpdateReadinessCondition(_ context.Context, _ corev1.ConditionStatus, _, _ string) error {
	return nil
}

func TestNewNodeNetworkConfigReconcilerRejectsTypedNilHealthChecker(t *testing.T) {
	t.Setenv("NODE_NAME", "test-node")

	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var typedNilHC *typedNilStubHealthChecker
	_, err := common.NewNodeNetworkConfigReconciler(
		c,
		logr.Discard(),
		noopConfigApplier{},
		filepath.Join(t.TempDir(), "config.yaml"),
		common.ReconcilerOptions{HealthChecker: typedNilHC},
	)
	if err == nil {
		t.Fatal("expected typed-nil health checker to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "typed-nil") {
		t.Fatalf("expected typed-nil error, got: %v", err)
	}
}
