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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlreconcile "sigs.k8s.io/controller-runtime/pkg/reconcile"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

const (
	podNetworkFinalizer = "network-connector.sylvaproject.org/podnetwork-coil-cleanup"

	// Calico's default IPAM block sizes. A block is the per-node slice a node
	// claims out of the pool; the network pools we create are large enough that
	// these defaults give sensible per-node distribution. blockSize must be
	// numerically >= the pool CIDR prefix length, so for pools smaller than the
	// default block we fall back to the pool prefix (see poolBlockSize).
	defaultIPv4BlockSize = int64(26)
	defaultIPv6BlockSize = int64(122)

	// dns1123SubdomainMaxLen is the maximum length of a Kubernetes object name
	// (RFC 1123 subdomain). Calico IPPool names must satisfy it.
	dns1123SubdomainMaxLen = 253
)

// PodNetworkReconciler watches PodNetwork resources and reconciles the Calico
// IPPools that back them. Unlike the Outbound flow (which allocates specific
// egress source addresses and creates a /32 or /128 host pool per address), a
// PodNetwork represents a whole pod network: one IPPool is created per address
// family covering the referenced Network's full CIDR, with natOutgoing=false so
// the fabric (not Calico) owns SNAT. Pods opt in to the pool via the
// cni.projectcalico.org/ipv4pools / ipv6pools annotation; the pool names are
// surfaced in PodNetwork.status.ipPools.
//
// This controller lives in the platform-coil binary alongside CoilReconciler
// because it shares the Calico IPPool ownership model, even though it is driven
// by a different CRD.
type PodNetworkReconciler struct {
	client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Log       logr.Logger
}

//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=podnetworks,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=podnetworks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=podnetworks/finalizers,verbs=update
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=networks,verbs=get;list;watch
// crd.projectcalico.org/ippools RBAC (create/update/delete) is in config/platform-coil/rbac.yaml.

// Reconcile handles PodNetwork create/update/delete events.
func (r *PodNetworkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	pn := &nc.PodNetwork{}
	if err := r.Get(ctx, req.NamespacedName, pn); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("error fetching PodNetwork: %w", err)
	}

	if !pn.DeletionTimestamp.IsZero() {
		return r.handlePodNetworkDeletion(ctx, pn, logger)
	}

	// Wait for the Calico IPPool CRD before adding a finalizer or creating pools.
	if ready, result, err := r.checkCalicoCRD(ctx, logger); !ready {
		return result, err
	}

	if !controllerutil.ContainsFinalizer(pn, podNetworkFinalizer) {
		controllerutil.AddFinalizer(pn, podNetworkFinalizer)
		if err := r.Update(ctx, pn); err != nil {
			return ctrl.Result{}, fmt.Errorf("error adding finalizer: %w", err)
		}
	}

	// Resolve the referenced Network's CIDRs (same namespace).
	ipv4CIDR, ipv6CIDR, err := r.resolveNetworkCIDRs(ctx, pn)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error resolving Network %q: %w", pn.Spec.NetworkRef, err)
	}

	// Build the desired pools (one per family the Network provides).
	desired := map[string]calicoPoolSpec{}
	if ipv4CIDR != "" {
		name := podNetworkPoolName(pn.Namespace, pn.Name, "v4", ipv4CIDR)
		desired[name] = calicoPoolSpec{cidr: ipv4CIDR, family: "v4", blockSize: poolBlockSize(ipv4CIDR, defaultIPv4BlockSize)}
	}
	if ipv6CIDR != "" {
		name := podNetworkPoolName(pn.Namespace, pn.Name, "v6", ipv6CIDR)
		desired[name] = calicoPoolSpec{cidr: ipv6CIDR, family: "v6", blockSize: poolBlockSize(ipv6CIDR, defaultIPv6BlockSize)}
	}

	poolNames := make([]string, 0, len(desired))
	for name, spec := range desired {
		if err := r.upsertPodNetworkPool(ctx, pn, name, spec, logger); err != nil {
			return ctrl.Result{}, err
		}
		poolNames = append(poolNames, name)
	}
	sort.Strings(poolNames)

	if err := r.prunePodNetworkPools(ctx, pn, desired, logger); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.updateIPPoolStatus(ctx, pn, poolNames); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// calicoPoolSpec captures the derived properties of a desired IPPool.
type calicoPoolSpec struct {
	cidr      string
	family    string
	blockSize int64
}

// resolveNetworkCIDRs looks up the Network referenced by the PodNetwork (in the
// same namespace) and returns its IPv4 and IPv6 CIDRs. A missing Network is not
// an error: pools cannot be created yet, so empty CIDRs are returned and the
// existing pools (if any) are pruned. The Network watch re-triggers this
// reconcile once the Network appears.
func (r *PodNetworkReconciler) resolveNetworkCIDRs(ctx context.Context, pn *nc.PodNetwork) (ipv4, ipv6 string, err error) {
	network := &nc.Network{}
	getErr := r.Get(ctx, types.NamespacedName{Name: pn.Spec.NetworkRef, Namespace: pn.Namespace}, network)
	if apierrors.IsNotFound(getErr) {
		return "", "", nil
	}
	if getErr != nil {
		return "", "", fmt.Errorf("error getting Network: %w", getErr)
	}
	if network.Spec.IPv4 != nil {
		ipv4 = network.Spec.IPv4.CIDR
	}
	if network.Spec.IPv6 != nil {
		ipv6 = network.Spec.IPv6.CIDR
	}
	return ipv4, ipv6, nil
}

// podNetworkPoolName derives a stable, DNS-safe, cluster-unique IPPool name from
// the PodNetwork's namespace/name, address family and CIDR. IPPools are
// cluster-scoped, so the namespace is folded into the hash to avoid collisions
// between equally named PodNetworks in different namespaces. The readable name
// portion is truncated so the full name stays within the DNS-1123 subdomain
// limit even for near-max-length PodNetwork names; the hash preserves uniqueness.
func podNetworkPoolName(namespace, name, family, cidr string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + name + "/" + cidr))
	const prefix = "pn-"
	suffix := fmt.Sprintf("-%s-%s", family, hex.EncodeToString(sum[:4]))
	if budget := dns1123SubdomainMaxLen - len(prefix) - len(suffix); len(name) > budget {
		name = name[:budget]
	}
	return prefix + name + suffix
}

// poolBlockSize returns a valid Calico blockSize for a pool: the default block
// size, unless the pool CIDR is smaller than a single default block, in which
// case the pool prefix length is used (blockSize must be >= the pool prefix).
func poolBlockSize(cidr string, defaultBlock int64) int64 {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return defaultBlock
	}
	ones, _ := ipNet.Mask.Size()
	if int64(ones) > defaultBlock {
		return int64(ones)
	}
	return defaultBlock
}

func (r *PodNetworkReconciler) upsertPodNetworkPool(ctx context.Context, pn *nc.PodNetwork, poolName string, spec calicoPoolSpec, logger logr.Logger) error {
	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(calicoIPPoolGVK)
	desired.SetName(poolName)
	desired.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by":                  managedByValue,
		"network-connector.sylvaproject.org/podnetwork": pn.Name,
		"network-connector.sylvaproject.org/namespace":  pn.Namespace,
		"network-connector.sylvaproject.org/family":     spec.family,
	})
	if err := unstructured.SetNestedField(desired.Object, spec.cidr, "spec", "cidr"); err != nil {
		return fmt.Errorf("error setting cidr: %w", err)
	}
	// natOutgoing=false: the fabric performs SNAT, not Calico. This is the whole
	// point of the pool -- it makes the pod network's addresses routable rather
	// than masqueraded behind the node IP.
	if err := unstructured.SetNestedField(desired.Object, false, "spec", "natOutgoing"); err != nil {
		return fmt.Errorf("error setting natOutgoing: %w", err)
	}
	if err := unstructured.SetNestedField(desired.Object, spec.blockSize, "spec", "blockSize"); err != nil {
		return fmt.Errorf("error setting blockSize: %w", err)
	}
	// !all() keeps the pool from being auto-selected for ordinary pods; pods opt
	// in explicitly via the cni.projectcalico.org/ipv4pools / ipv6pools
	// annotation. Without this the pool could steal addresses from the default
	// pod network.
	if err := unstructured.SetNestedField(desired.Object, "!all()", "spec", "nodeSelector"); err != nil {
		return fmt.Errorf("error setting nodeSelector: %w", err)
	}
	if err := unstructured.SetNestedStringSlice(desired.Object, []string{"Workload"}, "spec", "allowedUses"); err != nil {
		return fmt.Errorf("error setting allowedUses: %w", err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(calicoIPPoolGVK)
	err := r.Get(ctx, types.NamespacedName{Name: poolName}, existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating Calico IPPool for PodNetwork", "name", poolName, "cidr", spec.cidr, "podnetwork", pn.Name)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("error creating Calico IPPool %s: %w", poolName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("error getting IPPool: %w", err)
	}

	// blockSize is immutable in Calico once the pool has blocks; the Network CIDR
	// is itself immutable, so we never need to change it. Preserve the existing
	// blockSize to avoid a rejected update, and reconcile the remaining fields.
	if bs, found, _ := unstructured.NestedInt64(existing.Object, "spec", "blockSize"); found {
		if err := unstructured.SetNestedField(desired.Object, bs, "spec", "blockSize"); err != nil {
			return fmt.Errorf("error preserving blockSize: %w", err)
		}
	}
	existing.Object["spec"] = desired.Object["spec"]
	existing.SetLabels(desired.GetLabels())
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("error updating Calico IPPool %s: %w", poolName, err)
	}
	return nil
}

// prunePodNetworkPools deletes IPPools owned by this PodNetwork that are no
// longer desired (e.g. after a Network lost an address family, or the Network
// was removed).
func (r *PodNetworkReconciler) prunePodNetworkPools(ctx context.Context, pn *nc.PodNetwork, desired map[string]calicoPoolSpec, logger logr.Logger) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(calicoIPPoolGVK)
	if err := r.List(ctx, list, client.MatchingLabels{
		"network-connector.sylvaproject.org/podnetwork": pn.Name,
		"network-connector.sylvaproject.org/namespace":  pn.Namespace,
	}); err != nil {
		return fmt.Errorf("error listing Calico IPPools for prune: %w", err)
	}
	for i := range list.Items {
		name := list.Items[i].GetName()
		if _, keep := desired[name]; keep {
			continue
		}
		if err := r.Delete(ctx, &list.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("error deleting stale Calico IPPool %s: %w", name, err)
		}
		logger.Info("deleted stale Calico IPPool for PodNetwork", "name", name, "podnetwork", pn.Name)
	}
	return nil
}

// updateIPPoolStatus writes the desired pool names into status.ipPools using
// optimistic concurrency. Only the ipPools field is touched so it never clobbers
// the fields owned by the intent status updater (conditions, networkIPv4/6, vrfs).
// Re-fetches use the uncached APIReader: because the PodNetwork status is also
// written by the intent controller, the cache may be stale after a conflict, and
// retrying against it would just keep conflicting.
func (r *PodNetworkReconciler) updateIPPoolStatus(ctx context.Context, pn *nc.PodNetwork, poolNames []string) error {
	if stringSlicesEqual(pn.Status.IPPools, poolNames) {
		return nil
	}
	const maxRetries = 3
	key := types.NamespacedName{Name: pn.Name, Namespace: pn.Namespace}
	for attempt := 0; attempt < maxRetries; attempt++ {
		fresh := &nc.PodNetwork{}
		if err := r.APIReader.Get(ctx, key, fresh); err != nil {
			return fmt.Errorf("error re-fetching PodNetwork for status update: %w", err)
		}
		if stringSlicesEqual(fresh.Status.IPPools, poolNames) {
			return nil
		}
		fresh.Status.IPPools = poolNames
		err := r.Status().Update(ctx, fresh)
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return fmt.Errorf("error updating PodNetwork status: %w", err)
		}
	}
	return fmt.Errorf("status update conflict after %d retries for PodNetwork %s", maxRetries, key)
}

func (r *PodNetworkReconciler) handlePodNetworkDeletion(ctx context.Context, pn *nc.PodNetwork, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(pn, podNetworkFinalizer) {
		return ctrl.Result{}, nil
	}

	pools := &unstructured.UnstructuredList{}
	pools.SetGroupVersionKind(calicoIPPoolGVK)
	if err := r.List(ctx, pools, client.MatchingLabels{
		"network-connector.sylvaproject.org/podnetwork": pn.Name,
		"network-connector.sylvaproject.org/namespace":  pn.Namespace,
	}); err != nil {
		// If the Calico IPPool CRD has been uninstalled there is nothing left to
		// clean up; treat it as benign so the finalizer can still be removed and
		// the PodNetwork is not stuck terminating.
		if !apimeta.IsNoMatchError(err) {
			return ctrl.Result{}, fmt.Errorf("error listing Calico IPPools for deletion: %w", err)
		}
		logger.Info("Calico IPPool CRD not present during deletion, nothing to clean up", "podnetwork", pn.Name)
	}
	for i := range pools.Items {
		if err := r.Delete(ctx, &pools.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("error deleting Calico IPPool %s: %w", pools.Items[i].GetName(), err)
		}
		logger.Info("deleted Calico IPPool for PodNetwork", "name", pools.Items[i].GetName(), "podnetwork", pn.Name)
	}

	controllerutil.RemoveFinalizer(pn, podNetworkFinalizer)
	if err := r.Update(ctx, pn); err != nil {
		return ctrl.Result{}, fmt.Errorf("error removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// mapNetworkToPodNetworks maps a Network change to the PodNetworks (in the same
// namespace) that reference it, so pools are (re)created once the Network exists.
func (r *PodNetworkReconciler) mapNetworkToPodNetworks(ctx context.Context, obj client.Object) []ctrlreconcile.Request {
	logger := log.FromContext(ctx)

	var pnList nc.PodNetworkList
	if err := r.List(ctx, &pnList, client.InNamespace(obj.GetNamespace())); err != nil {
		logger.Error(err, "error listing podnetworks for network mapping")
		return nil
	}

	var requests []ctrlreconcile.Request
	for i := range pnList.Items {
		if pnList.Items[i].Spec.NetworkRef != obj.GetName() {
			continue
		}
		requests = append(requests, ctrlreconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      pnList.Items[i].Name,
				Namespace: pnList.Items[i].Namespace,
			},
		})
	}
	return requests
}

// checkCalicoCRD verifies that the Calico IPPool CRD is registered. Returns
// (true, _, nil) if ready, (false, requeueResult, nil) if the CRD is not yet
// registered (a benign, expected state during bootstrap), or (false, _, err)
// for any other List failure (e.g. RBAC Forbidden or a transient API error) so
// the controller requeues and surfaces the failure here rather than proceeding
// to create IPPools and failing later.
func (r *PodNetworkReconciler) checkCalicoCRD(ctx context.Context, logger logr.Logger) (bool, ctrl.Result, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(calicoIPPoolGVK)
	if err := r.List(ctx, list, client.Limit(1)); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.Info("Calico IPPool CRD not yet available, will retry", "gvk", calicoIPPoolGVK.String())
			return false, ctrl.Result{RequeueAfter: crdRequeueInterval}, nil
		}
		return false, ctrl.Result{}, fmt.Errorf("error checking Calico IPPool CRD: %w", err)
	}
	return true, ctrl.Result{}, nil
}

// SetupWithManager registers the PodNetwork controller.
func (r *PodNetworkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("podnetwork-coil-reconciler").
		For(&nc.PodNetwork{}).
		Watches(&nc.Network{}, handler.EnqueueRequestsFromMapFunc(r.mapNetworkToPodNetworks)).
		Complete(r); err != nil {
		return fmt.Errorf("error setting up podnetwork controller: %w", err)
	}
	return nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
