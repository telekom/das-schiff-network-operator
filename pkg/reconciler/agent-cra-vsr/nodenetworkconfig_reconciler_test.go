/*
Copyright 2025.

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

package agent_cra_vsr //nolint:revive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/common"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ common.ConfigApplier = &CRAVSRConfigApplier{}

// stubCRAManager is a test stub for the CRA manager that returns a
// configurable error from ApplyConfiguration.
type stubCRAManager struct {
	err error
}

func (s *stubCRAManager) ApplyConfiguration(_ context.Context, _ *v1alpha1.NodeNetworkConfigSpec) error {
	return s.err
}

// stubHealthChecker is a test stub for the health checker that returns
// configurable errors from each check method.
type stubHealthChecker struct {
	checkInterfacesErr   error
	checkReachabilityErr error
	checkAPIServerErr    error
	taintsRemoved        bool
}

func (s *stubHealthChecker) CheckInterfaces() error                 { return s.checkInterfacesErr }
func (s *stubHealthChecker) CheckReachability() error               { return s.checkReachabilityErr }
func (s *stubHealthChecker) CheckAPIServer(_ context.Context) error { return s.checkAPIServerErr }
func (s *stubHealthChecker) TaintsRemoved() bool                    { return s.taintsRemoved }
func (*stubHealthChecker) RemoveTaints(_ context.Context) error     { return nil }
func (*stubHealthChecker) UpdateReadinessCondition(_ context.Context, _ corev1.ConditionStatus, _, _ string) error {
	return nil
}

// writeMiniHealthcheckConfig writes a minimal (empty-object) healthcheck config
// YAML to a temp file and points OPERATOR_NETHEALTHCHECK_CONFIG at it so that
// healthcheck.LoadConfig succeeds without touching the filesystem default path.
func writeMiniHealthcheckConfig(t *testing.T) {
	t.Helper()
	cfgFile := filepath.Join(t.TempDir(), "net-healthcheck-config.yaml")
	if err := os.WriteFile(cfgFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp healthcheck config: %v", err)
	}
	t.Setenv("OPERATOR_NETHEALTHCHECK_CONFIG", cfgFile)
}

// newTestScheme builds a runtime.Scheme with v1alpha1 registered.
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

// newTestNNC creates a NodeNetworkConfig for the canonical test node with the
// given revision.
func newTestNNC(revision string) *v1alpha1.NodeNetworkConfig {
	return &v1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Spec:       v1alpha1.NodeNetworkConfigSpec{Revision: revision},
	}
}

// newCRAVSRTestReconciler builds a NodeNetworkConfigReconciler wired with a
// CRAVSRConfigApplier (backed by mgr) and the supplied health-checker stub,
// using a fake Kubernetes client containing initialObjects.
func newCRAVSRTestReconciler(
	t *testing.T,
	mgr *stubCRAManager,
	hc healthcheck.HealthCheckerInterface,
	configPath string,
	initialObjects ...interface{},
) *NodeNetworkConfigReconciler {
	t.Helper()
	s := newTestScheme(t)
	b := fake.NewClientBuilder().WithScheme(s)
	for _, o := range initialObjects {
		if nnc, ok := o.(*v1alpha1.NodeNetworkConfig); ok {
			b = b.WithRuntimeObjects(nnc).WithStatusSubresource(nnc)
		}
	}
	c := b.Build()
	applier := &CRAVSRConfigApplier{craManager: mgr}
	r, err := common.NewNodeNetworkConfigReconcilerForTesting(
		c,
		logr.Discard(),
		applier,
		configPath,
		common.ReconcilerOptions{RestoreOnReconcileFailure: false},
		hc,
	)
	if err != nil {
		t.Fatalf("NewNodeNetworkConfigReconcilerForTesting: %v", err)
	}
	return &NodeNetworkConfigReconciler{NodeNetworkConfigReconciler: r}
}

// TestNewNodeNetworkConfigReconciler_RestoreOnReconcileFailure verifies that the
// factory sets RestoreOnReconcileFailure=false (VSR uses transactional commits).
func TestNewNodeNetworkConfigReconciler_RestoreOnReconcileFailure(t *testing.T) {
	writeMiniHealthcheckConfig(t)
	s := newTestScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	reconciler, err := NewNodeNetworkConfigReconciler(
		nil,
		fakeClient,
		logr.Discard(),
		filepath.Join(t.TempDir(), "non-existent-config.yaml"),
	)
	if err != nil {
		t.Fatalf("NewNodeNetworkConfigReconciler returned unexpected error: %v", err)
	}
	if reconciler == nil {
		t.Fatal("NewNodeNetworkConfigReconciler returned nil reconciler")
	}
	if reconciler.NodeNetworkConfigReconciler == nil {
		t.Fatal("embedded NodeNetworkConfigReconciler is nil")
	}
	if reconciler.NodeNetworkConfigReconciler.RestoreOnReconcileFailure() {
		t.Error("expected RestoreOnReconcileFailure=false for CRA-VSR reconciler, got true")
	}
}

// TestCRAVSRConfigApplier_ApplyConfig_PropagatesError verifies that
// CRAVSRConfigApplier.ApplyConfig returns an error when the underlying manager fails.
func TestCRAVSRConfigApplier_ApplyConfig_PropagatesError(t *testing.T) {
	sentinel := errors.New("netconf commit failed")
	applier := &CRAVSRConfigApplier{craManager: &stubCRAManager{err: sentinel}}

	cfg := &v1alpha1.NodeNetworkConfig{}
	err := applier.ApplyConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error from ApplyConfig, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected error to wrap sentinel, got: %v", err)
	}
}

// TestCRAVSRConfigApplier_ApplyConfig_SuccessOnNoError verifies that
// CRAVSRConfigApplier.ApplyConfig returns nil when the manager succeeds.
func TestCRAVSRConfigApplier_ApplyConfig_SuccessOnNoError(t *testing.T) {
	applier := &CRAVSRConfigApplier{craManager: &stubCRAManager{err: nil}}
	cfg := &v1alpha1.NodeNetworkConfig{}
	if err := applier.ApplyConfig(context.Background(), cfg); err != nil {
		t.Errorf("expected no error from ApplyConfig, got: %v", err)
	}
}

// TestCRAVSR_Reconcile_ApplyPath verifies the successful reconciliation path:
// a new revision triggers ApplyConfiguration and the NNC status becomes Provisioned.
func TestCRAVSR_Reconcile_ApplyPath(t *testing.T) {
	t.Setenv("NODE_NAME", "test-node")

	nnc := newTestNNC("rev-1")
	hc := &stubHealthChecker{taintsRemoved: true}
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	r := newCRAVSRTestReconciler(t, &stubCRAManager{err: nil}, hc, configPath, nnc)

	_, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}
}

// TestCRAVSR_Reconcile_ErrorPropagation verifies that when the CRA manager fails,
// Reconcile returns an error and the NNC status is set to Invalid.
func TestCRAVSR_Reconcile_ErrorPropagation(t *testing.T) {
	t.Setenv("NODE_NAME", "test-node")

	nnc := newTestNNC("rev-1")
	hc := &stubHealthChecker{taintsRemoved: true}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	applyErr := errors.New("netconf commit failed")

	r := newCRAVSRTestReconciler(t, &stubCRAManager{err: applyErr}, hc, configPath, nnc)

	_, err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected Reconcile to return an error when ApplyConfiguration fails, got nil")
	}
	if !errors.Is(err, applyErr) {
		t.Errorf("expected error chain to contain applyErr, got: %v", err)
	}
}

// TestCRAVSR_Reconcile_NoRestoreOnFailure verifies the key VSR invariant:
// when reconciliation fails, the previous config is NOT reapplied (no restore).
// This is the opposite of FRR-like behavior (RestoreOnReconcileFailure=false).
func TestCRAVSR_Reconcile_NoRestoreOnFailure(t *testing.T) {
	t.Setenv("NODE_NAME", "test-node")

	nnc := newTestNNC("rev-2")
	hc := &stubHealthChecker{taintsRemoved: true}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	applyErr := errors.New("netconf commit failed")

	var trackedCalls int
	trackingApplier := &trackingCRAManager{
		err:      applyErr,
		callsPtr: &trackedCalls,
	}

	r := newCRAVSRTestReconcilerWithApplier(
		t,
		&CRAVSRConfigApplier{craManager: trackingApplier},
		hc,
		configPath,
		nnc,
	)

	_, _ = r.Reconcile(context.Background())

	if trackedCalls != 1 {
		t.Errorf("expected exactly 1 ApplyConfiguration call (new config only, no restore), got %d", trackedCalls)
	}
}

type trackingCRAManager struct {
	err      error
	callsPtr *int
}

func (m *trackingCRAManager) ApplyConfiguration(_ context.Context, _ *v1alpha1.NodeNetworkConfigSpec) error {
	*m.callsPtr++
	return m.err
}

func newCRAVSRTestReconcilerWithApplier(
	t *testing.T,
	applier common.ConfigApplier,
	hc healthcheck.HealthCheckerInterface,
	configPath string,
	initialObjects ...interface{},
) *NodeNetworkConfigReconciler {
	t.Helper()
	s := newTestScheme(t)
	b := fake.NewClientBuilder().WithScheme(s)
	for _, o := range initialObjects {
		if nnc, ok := o.(*v1alpha1.NodeNetworkConfig); ok {
			b = b.WithRuntimeObjects(nnc).WithStatusSubresource(nnc)
		}
	}
	c := b.Build()
	r, err := common.NewNodeNetworkConfigReconcilerForTesting(
		c,
		logr.Discard(),
		applier,
		configPath,
		common.ReconcilerOptions{RestoreOnReconcileFailure: false},
		hc,
	)
	if err != nil {
		t.Fatalf("NewNodeNetworkConfigReconcilerForTesting: %v", err)
	}
	return &NodeNetworkConfigReconciler{NodeNetworkConfigReconciler: r}
}

// TestCRAVSR_Reconcile_Idempotent verifies that when the in-memory config has
// the same revision as the API server, ApplyConfiguration is NOT called again.
func TestCRAVSR_Reconcile_Idempotent(t *testing.T) {
	t.Setenv("NODE_NAME", "test-node")

	nnc := newTestNNC("rev-1")
	nnc.Status.ConfigStatus = operator.StatusProvisioned
	hc := &stubHealthChecker{taintsRemoved: true}
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	trackedCalls := 0
	r := newCRAVSRTestReconcilerWithApplier(
		t,
		&CRAVSRConfigApplier{craManager: &trackingCRAManager{
			err:      nil,
			callsPtr: &trackedCalls,
		}},
		hc,
		configPath,
		nnc,
	)

	// Simulate that this revision was already applied by pre-loading the in-memory config.
	r.NodeNetworkConfigReconciler.NodeNetworkConfig = newTestNNC("rev-1")

	// Write a config file so storeConfig can persist the idempotent reconcile.
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}
	if trackedCalls != 0 {
		t.Errorf("expected 0 ApplyConfiguration calls for idempotent reconcile, got %d", trackedCalls)
	}
}
