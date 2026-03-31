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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	coilFinalizer = "network-connector.sylvaproject.org/coil-cleanup"
	coilNamespace = "coil-system"
)

// CoilReconciler watches Outbound and PodNetwork resources and reconciles
// Coil EgressNAT and AddressPool resources.
//
// This is a scaffold — Coil CRD types are not imported yet.
// Uses ConfigMap placeholders until proper Coil types are available.
type CoilReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=outbounds,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=outbounds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=outbounds/finalizers,verbs=update
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=podnetworks,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=podnetworks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=podnetworks/finalizers,verbs=update

// Reconcile handles Outbound create/update/delete events.
func (r *CoilReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Try Outbound first.
	outbound := &nc.Outbound{}
	if err := r.Get(ctx, req.NamespacedName, outbound); err == nil {
		return r.reconcileOutbound(ctx, outbound, logger)
	}

	// Try PodNetwork.
	podNetwork := &nc.PodNetwork{}
	if err := r.Get(ctx, req.NamespacedName, podNetwork); err == nil {
		return r.reconcilePodNetwork(ctx, podNetwork, logger)
	}

	return ctrl.Result{}, nil
}

func (r *CoilReconciler) reconcileOutbound(ctx context.Context, ob *nc.Outbound, logger logr.Logger) (ctrl.Result, error) {
	if !ob.DeletionTimestamp.IsZero() {
		return r.handleOutboundDeletion(ctx, ob, logger)
	}

	if !controllerutil.ContainsFinalizer(ob, coilFinalizer) {
		controllerutil.AddFinalizer(ob, coilFinalizer)
		if err := r.Update(ctx, ob); err != nil {
			return ctrl.Result{}, fmt.Errorf("error adding finalizer: %w", err)
		}
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("coil-egress-%s", ob.Name),
			Namespace: coilNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":           "network-connector",
				"network-connector.sylvaproject.org/type": "egressnat",
			},
		},
		Data: map[string]string{
			"outbound":   ob.Name,
			"networkRef": ob.Spec.NetworkRef,
		},
	}

	if ob.Spec.Replicas != nil {
		cm.Data["replicas"] = fmt.Sprintf("%d", *ob.Spec.Replicas)
	}

	return ctrl.Result{}, r.upsertConfigMap(ctx, cm, logger)
}

func (r *CoilReconciler) reconcilePodNetwork(ctx context.Context, pn *nc.PodNetwork, logger logr.Logger) (ctrl.Result, error) {
	if !pn.DeletionTimestamp.IsZero() {
		return r.handlePodNetworkDeletion(ctx, pn, logger)
	}

	if !controllerutil.ContainsFinalizer(pn, coilFinalizer) {
		controllerutil.AddFinalizer(pn, coilFinalizer)
		if err := r.Update(ctx, pn); err != nil {
			return ctrl.Result{}, fmt.Errorf("error adding finalizer: %w", err)
		}
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("coil-pool-%s", pn.Name),
			Namespace: coilNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":           "network-connector",
				"network-connector.sylvaproject.org/type": "addresspool",
			},
		},
		Data: map[string]string{
			"podnetwork": pn.Name,
			"networkRef": pn.Spec.NetworkRef,
		},
	}

	return ctrl.Result{}, r.upsertConfigMap(ctx, cm, logger)
}

func (r *CoilReconciler) handleOutboundDeletion(ctx context.Context, ob *nc.Outbound, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(ob, coilFinalizer) {
		return ctrl.Result{}, nil
	}

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: fmt.Sprintf("coil-egress-%s", ob.Name), Namespace: coilNamespace,
	}}
	if err := r.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("error deleting Coil egress placeholder: %w", err)
	}
	logger.Info("deleted Coil egress placeholder", "name", cm.Name)

	controllerutil.RemoveFinalizer(ob, coilFinalizer)
	return ctrl.Result{}, r.Update(ctx, ob)
}

func (r *CoilReconciler) handlePodNetworkDeletion(ctx context.Context, pn *nc.PodNetwork, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(pn, coilFinalizer) {
		return ctrl.Result{}, nil
	}

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: fmt.Sprintf("coil-pool-%s", pn.Name), Namespace: coilNamespace,
	}}
	if err := r.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("error deleting Coil pool placeholder: %w", err)
	}
	logger.Info("deleted Coil pool placeholder", "name", cm.Name)

	controllerutil.RemoveFinalizer(pn, coilFinalizer)
	return ctrl.Result{}, r.Update(ctx, pn)
}

func (r *CoilReconciler) upsertConfigMap(ctx context.Context, cm *corev1.ConfigMap, logger logr.Logger) error {
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating Coil placeholder", "name", cm.Name)
		return r.Create(ctx, cm)
	}
	if err != nil {
		return fmt.Errorf("error getting ConfigMap: %w", err)
	}

	existing.Data = cm.Data
	existing.Labels = cm.Labels
	return r.Update(ctx, existing)
}

// SetupWithManager registers the Coil controller.
func (r *CoilReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("coil-reconciler").
		For(&nc.Outbound{}).
		Watches(&nc.PodNetwork{}, &handler.EnqueueRequestForObject{}).
		Complete(r)
}
