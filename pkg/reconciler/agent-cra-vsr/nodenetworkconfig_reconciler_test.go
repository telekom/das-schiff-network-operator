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
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/common"
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

// TestNewNodeNetworkConfigReconciler_RestoreOnReconcileFailure verifies that the
// factory sets RestoreOnReconcileFailure=false (VSR uses transactional commits).
func TestNewNodeNetworkConfigReconciler_RestoreOnReconcileFailure(t *testing.T) {
	// Point OPERATOR_NETHEALTHCHECK_CONFIG at a valid temp file so that
	// healthcheck.LoadConfig treats it as mandatory (non-empty env var path) and
	// reads the empty-object YAML without error, ensuring full test isolation
	// regardless of what may or may not exist at the default system path.
	writeMiniHealthcheckConfig(t)

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add v1alpha1 to scheme: %v", err)
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	logger := logr.Discard()

	configPath := filepath.Join(t.TempDir(), "non-existent-config.yaml")

	// craManager is nil: NewNodeNetworkConfigReconciler only stores the reference
	// in CRAVSRConfigApplier and never calls ApplyConfig during construction, so
	// nil is safe here.
	reconciler, err := NewNodeNetworkConfigReconciler(
		nil,
		fakeClient,
		logger,
		configPath,
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
