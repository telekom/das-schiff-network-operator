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

package agent_netplan //nolint:revive

import (
	"context"
	"errors"
	"testing"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/controllers/shared"
	"github.com/telekom/das-schiff-network-operator/controllers/testutil"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fakeNodeNetplanConfigReconciler struct {
	err    error
	called bool
}

func (f *fakeNodeNetplanConfigReconciler) Reconcile(_ context.Context) error {
	f.called = true
	return f.err
}

func newNodeNetplanConfig(name string) client.Object {
	return &networkv1alpha1.NodeNetplanConfig{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestNamePredicate_CreateFunc(t *testing.T) {
	t.Setenv(healthcheck.NodenameEnv, "worker-node-01")
	testutil.RunNamePredicateCreateTests(t, shared.BuildNamePredicates(), newNodeNetplanConfig)
}

func TestNamePredicate_UpdateFunc(t *testing.T) {
	t.Setenv(healthcheck.NodenameEnv, "worker-node-01")
	testutil.RunNamePredicateUpdateTests(t, shared.BuildNamePredicates(), newNodeNetplanConfig)
}

func TestNamePredicate_DeleteAndGenericReturnFalse(t *testing.T) {
	testutil.RunDeleteAndGenericAlwaysFalse(t, shared.BuildNamePredicates())
}

func TestNamePredicate_EmptyNodeName(t *testing.T) {
	t.Setenv(healthcheck.NodenameEnv, "")
	testutil.RunEmptyNodeNameTest(t, shared.BuildNamePredicates(), newNodeNetplanConfig)
}

func TestNamePredicate_NilObject(t *testing.T) {
	t.Setenv(healthcheck.NodenameEnv, "worker-node-01")
	testutil.RunNilObjectSafetyTest(t, shared.BuildNamePredicates())
}

func TestReconcile_NilReconcilerReturnsError(t *testing.T) {
	r := &NodeNetplanConfigReconciler{}
	_, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err == nil {
		t.Error("expected error when Reconciler is nil, got none")
	}
}

func TestReconcile_DelegatesAndReturnsDefaultRequeue(t *testing.T) {
	fake := &fakeNodeNetplanConfigReconciler{err: nil}
	r := &NodeNetplanConfigReconciler{Reconciler: fake}

	result, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.called {
		t.Error("expected inner Reconcile to be called")
	}
	if result.RequeueAfter != requeueTime {
		t.Errorf("expected RequeueAfter=%v, got %v", requeueTime, result.RequeueAfter)
	}
}

func TestReconcile_PropagatesInnerError(t *testing.T) {
	fake := &fakeNodeNetplanConfigReconciler{err: errors.New("inner error")}
	r := &NodeNetplanConfigReconciler{Reconciler: fake}

	_, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err == nil {
		t.Error("expected error from inner Reconcile, got nil")
	}
}
