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

package agent_cra_grout //nolint:revive

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/controllers/shared"
	reconcilergrout "github.com/telekom/das-schiff-network-operator/pkg/reconciler/agent-cra-grout"
)

const requeueTime = 10 * time.Minute

// NodeNetworkConfigReconciler reconciles a NodeNetworkConfig object for cra-grout.
type NodeNetworkConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Reconciler *reconcilergrout.NodeNetworkConfigReconciler
}

//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=nodenetworkconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=nodenetworkconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=nodenetworkconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=noderoutedports,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=core,resources=nodes/status,verbs=get;update;patch

// Reconcile drives the cra-grout reconciliation loop.
func (r *NodeNetworkConfigReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	if r.Reconciler == nil {
		return ctrl.Result{}, fmt.Errorf("reconciler is not initialized")
	}

	result, err := r.Reconciler.Reconcile(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciliation error: %w", err)
	}

	if result.RequeueAfter > 0 {
		return result, nil
	}

	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeNetworkConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		For(&networkv1alpha1.NodeNetworkConfig{}, builder.WithPredicates(shared.BuildNamePredicates())).
		// Re-render when this node's routed CNI attachments change: they arrive
		// via the aggregate per-node NodeRoutedPorts object (out-of-band from the
		// NodeNetworkConfig revision), so a change there must trigger a reconcile.
		Watches(&networkv1alpha1.NodeRoutedPorts{},
			handler.EnqueueRequestsFromMapFunc(r.mapNodeRoutedPorts),
			builder.WithPredicates(shared.BuildNamePredicates())).
		Complete(r)
	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}
	return nil
}

// mapNodeRoutedPorts enqueues a reconcile for the node's NodeNetworkConfig when
// its NodeRoutedPorts object changes (they share the node name).
func (*NodeNetworkConfigReconciler) mapNodeRoutedPorts(_ context.Context, obj client.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: obj.GetName()},
	}}
}
