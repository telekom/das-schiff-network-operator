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
	"fmt"
	"time"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/controllers/shared"
	agentcrafrr "github.com/telekom/das-schiff-network-operator/pkg/reconciler/agent-cra-frr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const requeueTime = 10 * time.Minute

type nodeNetworkConfigReconciler interface {
	Reconcile(ctx context.Context) (ctrl.Result, error)
}

var _ nodeNetworkConfigReconciler = (*agentcrafrr.NodeNetworkConfigReconciler)(nil)

// NodeNetworkConfigReconciler reconciles a NodeNetworkConfig object.
type NodeNetworkConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Reconciler nodeNetworkConfigReconciler
}

//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=nodenetworkconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=nodenetworkconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=nodenetworkconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=core,resources=nodes/status,verbs=get;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.4/pkg/reconcile
func (r *NodeNetworkConfigReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	if r.Reconciler == nil {
		return ctrl.Result{}, fmt.Errorf("reconciler is not initialized")
	}

	// Run ReconcileDebounced through debouncer
	result, err := r.Reconciler.Reconcile(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciliation error: %w", err)
	}

	// If the reconciler requested a specific requeue, use that
	if result.RequeueAfter > 0 {
		return result, nil
	}

	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeNetworkConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		For(&networkv1alpha1.NodeNetworkConfig{}, builder.WithPredicates(shared.BuildNamePredicates())).
		Complete(r)
	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}
	return nil
}
