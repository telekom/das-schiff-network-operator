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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
// IPAddressPool + L2/BGPAdvertisement resources using unstructured objects.
type MetalLBReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=inbounds,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=inbounds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=inbounds/finalizers,verbs=update
//+kubebuilder:rbac:groups=metallb.io,resources=ipaddresspools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=metallb.io,resources=bgpadvertisements,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=metallb.io,resources=l2advertisements,verbs=get;list;watch;create;update;patch;delete

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

	// Check target CRDs exist before adding finalizer or creating resources.
	if ready, result := r.checkTargetCRDs(ctx, logger); !ready {
		return result, nil
	}

	// Ensure finalizer.
	if !controllerutil.ContainsFinalizer(inbound, metallbFinalizer) {
		controllerutil.AddFinalizer(inbound, metallbFinalizer)
		if err := r.Update(ctx, inbound); err != nil {
			return ctrl.Result{}, fmt.Errorf("error adding finalizer: %w", err)
		}
	}

	// Reconcile the MetalLB resources.
	if err := r.reconcilePool(ctx, inbound, logger); err != nil {
		return ctrl.Result{}, fmt.Errorf("error reconciling MetalLB pool: %w", err)
	}

	return ctrl.Result{}, nil
}

// resolvePoolName returns the explicit pool name or falls back to the Inbound name.
func resolvePoolName(inbound *nc.Inbound) string {
	if inbound.Spec.PoolName != nil {
		return *inbound.Spec.PoolName
	}
	return inbound.Name
}

// collectAddresses prefers Status.Addresses (IPAM-resolved), falling back to Spec.Addresses.
func collectAddresses(inbound *nc.Inbound) []interface{} {
	src := inbound.Status.Addresses
	if src == nil {
		src = inbound.Spec.Addresses
	}
	if src == nil {
		return nil
	}
	addrs := make([]interface{}, 0, len(src.IPv4)+len(src.IPv6))
	for _, a := range src.IPv4 {
		addrs = append(addrs, a)
	}
	for _, a := range src.IPv6 {
		addrs = append(addrs, a)
	}
	return addrs
}

// managedLabels returns labels applied to every MetalLB object we create.
func managedLabels(inboundName string) map[string]interface{} {
	return map[string]interface{}{
		"app.kubernetes.io/managed-by":              "network-connector",
		"network-connector.sylvaproject.org/inbound": inboundName,
	}
}

// newIPAddressPool builds an unstructured IPAddressPool.
func newIPAddressPool(poolName, inboundName string, addresses []interface{}) *unstructured.Unstructured {
	pool := &unstructured.Unstructured{}
	pool.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "metallb.io",
		Version: "v1beta1",
		Kind:    "IPAddressPool",
	})
	pool.SetName(poolName)
	pool.SetNamespace(metallbNamespace)
	pool.SetLabels(toStringLabels(managedLabels(inboundName)))
	if err := unstructured.SetNestedSlice(pool.Object, addresses, "spec", "addresses"); err != nil {
		// addresses is always []interface{} of strings; this cannot fail in practice.
		panic(fmt.Sprintf("bug: SetNestedSlice: %v", err))
	}
	return pool
}

// newAdvertisement builds an unstructured BGPAdvertisement or L2Advertisement.
func newAdvertisement(kind, poolName, inboundName string) *unstructured.Unstructured {
	adv := &unstructured.Unstructured{}
	adv.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "metallb.io",
		Version: "v1beta1",
		Kind:    kind,
	})
	adv.SetName(poolName)
	adv.SetNamespace(metallbNamespace)
	adv.SetLabels(toStringLabels(managedLabels(inboundName)))
	if err := unstructured.SetNestedSlice(adv.Object, []interface{}{poolName}, "spec", "ipAddressPools"); err != nil {
		panic(fmt.Sprintf("bug: SetNestedSlice: %v", err))
	}
	return adv
}

// advertisementKind maps the Inbound advertisement type to the MetalLB CRD kind.
func advertisementKind(advType string) string {
	if advType == "l2" {
		return "L2Advertisement"
	}
	return "BGPAdvertisement"
}

// reconcilePool creates or updates IPAddressPool + Advertisement for an Inbound.
func (r *MetalLBReconciler) reconcilePool(ctx context.Context, inbound *nc.Inbound, logger logr.Logger) error {
	poolName := resolvePoolName(inbound)
	addresses := collectAddresses(inbound)
	kind := advertisementKind(inbound.Spec.Advertisement.Type)

	// Reconcile IPAddressPool.
	desiredPool := newIPAddressPool(poolName, inbound.Name, addresses)
	if err := r.applyUnstructured(ctx, desiredPool, logger); err != nil {
		return fmt.Errorf("error reconciling IPAddressPool %q: %w", poolName, err)
	}

	// Reconcile Advertisement.
	desiredAdv := newAdvertisement(kind, poolName, inbound.Name)
	if err := r.applyUnstructured(ctx, desiredAdv, logger); err != nil {
		return fmt.Errorf("error reconciling %s %q: %w", kind, poolName, err)
	}

	// Update Inbound status with resolved pool name.
	inbound.Status.PoolName = &poolName
	if err := r.Status().Update(ctx, inbound); err != nil {
		return fmt.Errorf("error updating Inbound status: %w", err)
	}

	return nil
}

// applyUnstructured creates or updates an unstructured object.
func (r *MetalLBReconciler) applyUnstructured(ctx context.Context, desired *unstructured.Unstructured, logger logr.Logger) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())

	key := types.NamespacedName{Name: desired.GetName(), Namespace: desired.GetNamespace()}
	err := r.Get(ctx, key, existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating MetalLB resource", "kind", desired.GetKind(), "name", desired.GetName())
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("error getting %s %q: %w", desired.GetKind(), desired.GetName(), err)
	}

	// Preserve resource version for update.
	desired.SetResourceVersion(existing.GetResourceVersion())
	logger.Info("updating MetalLB resource", "kind", desired.GetKind(), "name", desired.GetName())
	return r.Update(ctx, desired)
}

func (r *MetalLBReconciler) handleDeletion(ctx context.Context, inbound *nc.Inbound, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(inbound, metallbFinalizer) {
		return ctrl.Result{}, nil
	}

	poolName := resolvePoolName(inbound)
	kind := advertisementKind(inbound.Spec.Advertisement.Type)

	// Delete the IPAddressPool.
	pool := &unstructured.Unstructured{}
	pool.SetGroupVersionKind(schema.GroupVersionKind{Group: "metallb.io", Version: "v1beta1", Kind: "IPAddressPool"})
	pool.SetName(poolName)
	pool.SetNamespace(metallbNamespace)
	if err := r.Delete(ctx, pool); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("error deleting IPAddressPool %q: %w", poolName, err)
	}
	logger.Info("deleted IPAddressPool", "name", poolName)

	// Delete the Advertisement.
	adv := &unstructured.Unstructured{}
	adv.SetGroupVersionKind(schema.GroupVersionKind{Group: "metallb.io", Version: "v1beta1", Kind: kind})
	adv.SetName(poolName)
	adv.SetNamespace(metallbNamespace)
	if err := r.Delete(ctx, adv); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("error deleting %s %q: %w", kind, poolName, err)
	}
	logger.Info("deleted Advertisement", "kind", kind, "name", poolName)

	controllerutil.RemoveFinalizer(inbound, metallbFinalizer)
	if err := r.Update(ctx, inbound); err != nil {
		return ctrl.Result{}, fmt.Errorf("error removing finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

// toStringLabels converts map[string]interface{} to map[string]string.
func toStringLabels(m map[string]interface{}) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// SetupWithManager registers the MetalLB controller.
func (r *MetalLBReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("metallb-reconciler").
		For(&nc.Inbound{}).
		Complete(r); err != nil {
		return fmt.Errorf("error setting up metallb controller: %w", err)
	}
	return nil
}

var metallbTargetGVKs = []schema.GroupVersionKind{
	{Group: "metallb.io", Version: "v1beta1", Kind: "IPAddressPool"},
	{Group: "metallb.io", Version: "v1beta1", Kind: "BGPAdvertisement"},
	{Group: "metallb.io", Version: "v1beta1", Kind: "L2Advertisement"},
}

// checkTargetCRDs verifies that MetalLB CRDs are registered.
// Returns (true, _) if ready, or (false, requeueResult) if not.
func (r *MetalLBReconciler) checkTargetCRDs(ctx context.Context, logger logr.Logger) (bool, ctrl.Result) {
	for _, gvk := range metallbTargetGVKs {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		if err := r.List(ctx, list, client.Limit(1)); err != nil {
			if apimeta.IsNoMatchError(err) {
				logger.Info("target CRD not yet available, will retry", "gvk", gvk.String())
				return false, ctrl.Result{RequeueAfter: crdRequeueInterval}
			}
		}
	}
	return true, ctrl.Result{}
}
