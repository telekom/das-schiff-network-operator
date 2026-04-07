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

package intent

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	intentreconciler "github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent"
)

const requeueTime = 10 * time.Minute

// Controller watches all intent CRDs and fans out to the intent reconciler.
type Controller struct {
	client.Client
	Scheme     *runtime.Scheme
	Reconciler *intentreconciler.Reconciler
}

//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=vrfs,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=vrfs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=vrfs/finalizers,verbs=update
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=networks,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=networks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=networks/finalizers,verbs=update
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=destinations,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=destinations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=destinations/finalizers,verbs=update
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=layer2attachments,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=layer2attachments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=inbounds,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=inbounds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=outbounds,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=outbounds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=podnetworks,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=podnetworks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=bgppeerings,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=bgppeerings/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=collectors,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=collectors/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=collectors/finalizers,verbs=update
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=trafficmirrors,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=trafficmirrors/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=announcementpolicies,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=announcementpolicies/status,verbs=get;update;patch

// Reconcile handles any intent CRD change by triggering the debounced reconciler.
func (r *Controller) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("intent CRD change detected, triggering debounced reconciliation")

	r.Reconciler.Reconcile(ctx)

	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

// SetupWithManager registers watches for all intent CRDs and Node.
func (r *Controller) SetupWithManager(mgr ctrl.Manager) error {
	// Generic handler: any change in any watched type triggers a single reconcile.
	h := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, _ client.Object) []reconcile.Request {
			return []ctrl.Request{{}}
		},
	)

	err := ctrl.NewControllerManagedBy(mgr).
		Named("intent-reconciler").
		Watches(&nc.VRF{}, h).
		Watches(&nc.Network{}, h).
		Watches(&nc.Destination{}, h).
		Watches(&nc.Layer2Attachment{}, h).
		Watches(&nc.Inbound{}, h).
		Watches(&nc.Outbound{}, h).
		Watches(&nc.PodNetwork{}, h).
		Watches(&nc.BGPPeering{}, h).
		Watches(&nc.Collector{}, h).
		Watches(&nc.TrafficMirror{}, h).
		Watches(&nc.AnnouncementPolicy{}, h).
		Watches(&corev1.Node{}, h).
		Complete(r)
	if err != nil {
		return fmt.Errorf("error creating intent controller: %w", err)
	}

	return nil
}
