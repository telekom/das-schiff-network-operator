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

package operator

import (
	"context"
	"fmt"
	"time"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const requeueTime = 10 * time.Minute

// ConfigReconciler reconciles a Layer2NetworkConfiguration, RoutingTable and VRFRouteConfiguration objects.
type ConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Reconciler *operator.ConfigReconciler
}

//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=layer2networkconfigurations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=layer2networkconfigurations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=layer2networkconfigurations/finalizers,verbs=update

//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=vrfrouteconfigurations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=vrfrouteconfigurations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=vrfrouteconfigurations/finalizers,verbs=update

//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations;validatingwebhookconfigurations,verbs=get;list;watch;update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *ConfigReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	r.Reconciler.Reconcile(ctx)

	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	h := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, _ client.Object) []reconcile.Request { return []ctrl.Request{{}} })
	err := ctrl.NewControllerManagedBy(mgr).
		Named("config controller").
		Watches(&networkv1alpha1.Layer2NetworkConfiguration{}, h).
		Watches(&networkv1alpha1.VRFRouteConfiguration{}, h).
		Complete(r)
	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}
	return nil
}
