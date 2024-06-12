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

package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// NodeConfigReconciler reconciles a NodeConfig object.
type NodeConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Reconciler *reconciler.Reconciler
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=nodeconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=nodeconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=nodeconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.4/pkg/reconcile
func (r *NodeConfigReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Run ReconcileDebounced through debouncer
	r.Reconciler.Reconcile(ctx)

	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	namePredicates := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return strings.Contains(e.Object.GetName(), os.Getenv(healthcheck.NodenameEnv))
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return strings.Contains(e.ObjectNew.GetName(), os.Getenv(healthcheck.NodenameEnv))
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}

	err := ctrl.NewControllerManagedBy(mgr).
		For(&networkv1alpha1.NodeConfig{}, builder.WithPredicates(namePredicates)).
		Complete(r)
	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}
	return nil
}
