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

package platform

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	metallbFinalizer = "network-connector.sylvaproject.org/metallb-cleanup"
	metallbNamespace = "metallb-system"
)

// MetalLBReconciler watches Inbound resources and reconciles MetalLB
// IPAddressPool + L2/BGPAdvertisement resources.
//
// This is a scaffold — MetalLB CRD types are not imported yet.
// The reconciler creates ConfigMaps as placeholders that a future
// MetalLB integration will replace with proper typed resources.
type MetalLBReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=inbounds,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=inbounds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=inbounds/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles Inbound create/update/delete events.
func (r *MetalLBReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	inbound := &nc.Inbound{}
	if err := r.Get(ctx, req.NamespacedName, inbound); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("error fetching Inbound: %w", err)
	}

	// Handle deletion.
	if !inbound.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, inbound, logger)
	}

	// Ensure finalizer.
	if !controllerutil.ContainsFinalizer(inbound, metallbFinalizer) {
		controllerutil.AddFinalizer(inbound, metallbFinalizer)
		if err := r.Update(ctx, inbound); err != nil {
			return ctrl.Result{}, fmt.Errorf("error adding finalizer: %w", err)
		}
	}

	// Reconcile the MetalLB resources (placeholder: ConfigMap).
	if err := r.reconcilePool(ctx, inbound, logger); err != nil {
		return ctrl.Result{}, fmt.Errorf("error reconciling MetalLB pool: %w", err)
	}

	return ctrl.Result{}, nil
}

// reconcilePool creates or updates a placeholder ConfigMap representing
// the MetalLB IPAddressPool. Will be replaced with proper MetalLB CRD types.
func (r *MetalLBReconciler) reconcilePool(ctx context.Context, inbound *nc.Inbound, logger logr.Logger) error {
	poolName := inbound.Name
	if inbound.Spec.PoolName != nil {
		poolName = *inbound.Spec.PoolName
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("metallb-pool-%s", poolName),
			Namespace: metallbNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":           "network-connector",
				"network-connector.sylvaproject.org/type": "ipaddresspool",
			},
		},
		Data: map[string]string{
			"inbound":           inbound.Name,
			"networkRef":        inbound.Spec.NetworkRef,
			"advertisementType": inbound.Spec.Advertisement.Type,
		},
	}

	// Collect addresses.
	if inbound.Spec.Addresses != nil {
		for i, addr := range inbound.Spec.Addresses.IPv4 {
			cm.Data[fmt.Sprintf("ipv4-%d", i)] = addr
		}
		for i, addr := range inbound.Spec.Addresses.IPv6 {
			cm.Data[fmt.Sprintf("ipv6-%d", i)] = addr
		}
	}

	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating MetalLB pool placeholder", "name", cm.Name)
		return r.Create(ctx, cm)
	}
	if err != nil {
		return fmt.Errorf("error getting ConfigMap: %w", err)
	}

	existing.Data = cm.Data
	existing.Labels = cm.Labels
	return r.Update(ctx, existing)
}

func (r *MetalLBReconciler) handleDeletion(ctx context.Context, inbound *nc.Inbound, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(inbound, metallbFinalizer) {
		return ctrl.Result{}, nil
	}

	poolName := inbound.Name
	if inbound.Spec.PoolName != nil {
		poolName = *inbound.Spec.PoolName
	}

	// Delete the placeholder ConfigMap.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("metallb-pool-%s", poolName),
			Namespace: metallbNamespace,
		},
	}
	if err := r.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("error deleting MetalLB pool placeholder: %w", err)
	}
	logger.Info("deleted MetalLB pool placeholder", "name", cm.Name)

	controllerutil.RemoveFinalizer(inbound, metallbFinalizer)
	if err := r.Update(ctx, inbound); err != nil {
		return ctrl.Result{}, fmt.Errorf("error removing finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the MetalLB controller.
func (r *MetalLBReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("metallb-reconciler").
		For(&nc.Inbound{}).
		Complete(r)
}
