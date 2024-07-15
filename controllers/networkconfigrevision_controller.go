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

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// NetworkConfigRevisionReconciler reconciles a NodeConfig object.
type NetworkConfigRevisionReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Reconciler *reconciler.NodeConfigReconciler
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=networkconfigrevisions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=networkconfigrevisions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=networkconfigrevisions/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.4/pkg/reconcile
func (r *NetworkConfigRevisionReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Run ReconcileDebounced through debouncer
	r.Reconciler.Reconcile(ctx)

	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NetworkConfigRevisionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	predicates := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}

	err := ctrl.NewControllerManagedBy(mgr).
		For(&networkv1alpha1.NetworkConfigRevision{}).WithEventFilter(predicates).
		Complete(r)
	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}
	return nil
}
