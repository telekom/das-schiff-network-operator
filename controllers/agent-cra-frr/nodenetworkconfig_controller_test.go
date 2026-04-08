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

package agent_cra_frr //nolint:revive

import (
	"context"
	"testing"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/controllers/shared"
	"github.com/telekom/das-schiff-network-operator/controllers/testutil"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newNodeNetworkConfig(name string) client.Object {
	return &networkv1alpha1.NodeNetworkConfig{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestNamePredicate_CreateFunc(t *testing.T) {
	t.Setenv(healthcheck.NodenameEnv, "worker-node-01")
	testutil.RunNamePredicateCreateTests(t, shared.BuildNamePredicates(), newNodeNetworkConfig)
}

func TestNamePredicate_UpdateFunc(t *testing.T) {
	t.Setenv(healthcheck.NodenameEnv, "worker-node-01")
	testutil.RunNamePredicateUpdateTests(t, shared.BuildNamePredicates(), newNodeNetworkConfig)
}

func TestNamePredicate_DeleteAndGenericReturnFalse(t *testing.T) {
	testutil.RunDeleteAndGenericAlwaysFalse(t, shared.BuildNamePredicates())
}

func TestNamePredicate_EmptyNodeName(t *testing.T) {
	t.Setenv(healthcheck.NodenameEnv, "")
	testutil.RunEmptyNodeNameTest(t, shared.BuildNamePredicates(), newNodeNetworkConfig)
}

func TestReconcile_NilReconcilerReturnsError(t *testing.T) {
	r := &NodeNetworkConfigReconciler{}
	_, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err == nil {
		t.Error("expected error when Reconciler is nil, got none")
	}
}
