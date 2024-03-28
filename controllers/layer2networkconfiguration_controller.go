/*
Copyright 2022.

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
	"time"

	"github.com/google/go-cmp/cmp"
	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const requeueTime = 10 * time.Minute

// Layer2NetworkConfigurationReconciler reconciles a Layer2NetworkConfiguration object.
type Layer2NetworkConfigurationReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Reconciler *reconciler.ConfigReconciler
}

//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;update;watch
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=layer2networkconfigurations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=layer2networkconfigurations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=layer2networkconfigurations/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *Layer2NetworkConfigurationReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	r.Reconciler.Reconcile(ctx)

	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Layer2NetworkConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create empty request for changes to node
	nodesMapFn := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, _ client.Object) []reconcile.Request { return []reconcile.Request{{}} })
	nodePredicates := predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return os.Getenv(healthcheck.NodenameEnv) == e.ObjectNew.GetName() && !cmp.Equal(e.ObjectNew.GetLabels(), e.ObjectOld.GetLabels())
		},
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}

	err := ctrl.NewControllerManagedBy(mgr).
		For(&networkv1alpha1.Layer2NetworkConfiguration{}).
		Watches(&corev1.Node{}, nodesMapFn, builder.WithPredicates(nodePredicates)).
		Complete(r)
	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}
	return nil
}
