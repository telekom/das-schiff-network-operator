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
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/common"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ common.ConfigApplier = &CRAVSRConfigApplier{}

// TestNewNodeNetworkConfigReconciler_RestoreOnReconcileFailure verifies that the
// factory sets RestoreOnReconcileFailure=false (VSR uses transactional commits).
func TestNewNodeNetworkConfigReconciler_RestoreOnReconcileFailure(t *testing.T) {
	// Ensure OPERATOR_NETHEALTHCHECK_CONFIG is not set to a real path, which would
	// cause healthcheck.LoadConfig to treat the file as mandatory and fail the test.
	// When the env var is empty, LoadConfig uses the built-in default path
	// (/opt/network-operator/net-healthcheck-config.yaml) in non-mandatory mode
	// and silently skips it when the file does not exist (dev/CI environments).
	t.Setenv("OPERATOR_NETHEALTHCHECK_CONFIG", "")

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
