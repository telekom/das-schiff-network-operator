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
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlreconcile "sigs.k8s.io/controller-runtime/pkg/reconcile"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

const (
	coilFinalizer      = "network-connector.sylvaproject.org/coil-cleanup"
	ipv4HostPrefixLen  = 32
	ipv6HostPrefixLen  = 128
	crdRequeueInterval = 30 * time.Second

	// managedByValue is the label value for app.kubernetes.io/managed-by across all platform controllers.
	managedByValue = "network-connector"
)

var (
	calicoIPPoolGVK = schema.GroupVersionKind{Group: "crd.projectcalico.org", Version: "v1", Kind: "IPPool"}
	coilEgressGVK   = schema.GroupVersionKind{Group: "coil.cybozu.com", Version: "v2", Kind: "Egress"}
)

// CoilReconciler watches Outbound resources and reconciles
// Calico IPPool and Coil Egress resources.
type CoilReconciler struct {
	client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Log       logr.Logger
}

//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=outbounds,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=outbounds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=outbounds/finalizers,verbs=update
// coil.cybozu.com/egresses and crd.projectcalico.org/ippools RBAC is in config/platform-coil/rbac.yaml.
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=destinations,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=nodenetworkconfigs,verbs=get;list;watch

// Reconcile handles Outbound create/update/delete events.
func (r *CoilReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	outbound := &nc.Outbound{}
	if err := r.Get(ctx, req.NamespacedName, outbound); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("error fetching Outbound: %w", err)
	}

	if !outbound.DeletionTimestamp.IsZero() {
		return r.handleOutboundDeletion(ctx, outbound, logger)
	}

	// Check target CRDs exist before adding finalizer or creating resources.
	if ready, result := r.checkTargetCRDs(ctx, logger); !ready {
		return result, nil
	}

	if !controllerutil.ContainsFinalizer(outbound, coilFinalizer) {
		controllerutil.AddFinalizer(outbound, coilFinalizer)
		if err := r.Update(ctx, outbound); err != nil {
			return ctrl.Result{}, fmt.Errorf("error adding finalizer: %w", err)
		}
	}

	// Resolve destination prefixes.
	prefixes, err := r.resolveDestinations(ctx, outbound)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error resolving destinations: %w", err)
	}

	// Determine addresses: prefer status, fallback to spec.
	addresses := outbound.Status.Addresses
	if addresses == nil {
		addresses = outbound.Spec.Addresses
	}

	// Reconcile Calico IPPools.
	if addresses != nil {
		if len(addresses.IPv4) > 0 {
			if err := r.upsertCalicoIPPool(ctx, outbound, addresses.IPv4[0], ipv4HostPrefixLen, "v4", logger); err != nil {
				return ctrl.Result{}, fmt.Errorf("error reconciling IPv4 IPPool: %w", err)
			}
		}
		if len(addresses.IPv6) > 0 {
			if err := r.upsertCalicoIPPool(ctx, outbound, addresses.IPv6[0], ipv6HostPrefixLen, "v6", logger); err != nil {
				return ctrl.Result{}, fmt.Errorf("error reconciling IPv6 IPPool: %w", err)
			}
		}
	}

	// Reconcile Coil Egress.
	if err := r.upsertCoilEgress(ctx, outbound, prefixes, addresses, logger); err != nil {
		return ctrl.Result{}, fmt.Errorf("error reconciling Coil Egress: %w", err)
	}

	// Reconcile egress NetworkPolicy for the Coil gateway pods.
	if err := r.upsertEgressNetworkPolicy(ctx, outbound, prefixes, logger); err != nil {
		return ctrl.Result{}, fmt.Errorf("error reconciling egress NetworkPolicy: %w", err)
	}

	return ctrl.Result{}, nil
}

// resolveDestinations lists all Destination CRDs and filters by the Outbound's label selector,
// returning all matching prefixes.
func (r *CoilReconciler) resolveDestinations(ctx context.Context, ob *nc.Outbound) ([]string, error) {
	if ob.Spec.Destinations == nil {
		return nil, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(ob.Spec.Destinations)
	if err != nil {
		return nil, fmt.Errorf("error parsing destination selector: %w", err)
	}

	var destList nc.DestinationList
	if err := r.List(ctx, &destList); err != nil {
		return nil, fmt.Errorf("error listing destinations: %w", err)
	}

	var prefixes []string
	for i := range destList.Items {
		if selector.Matches(labels.Set(destList.Items[i].Labels)) {
			prefixes = append(prefixes, destList.Items[i].Spec.Prefixes...)
		}
	}
	return prefixes, nil
}

func (r *CoilReconciler) upsertCalicoIPPool(ctx context.Context, ob *nc.Outbound, cidr string, blockSize int64, suffix string, logger logr.Logger) error {
	poolName := fmt.Sprintf("%s-%s", ob.Name, suffix)

	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(calicoIPPoolGVK)
	desired.SetName(poolName)
	desired.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by":                managedByValue,
		"network-connector.sylvaproject.org/outbound": ob.Name,
	})
	if err := unstructured.SetNestedField(desired.Object, cidr, "spec", "cidr"); err != nil {
		return fmt.Errorf("error setting cidr: %w", err)
	}
	if err := unstructured.SetNestedField(desired.Object, false, "spec", "natOutgoing"); err != nil {
		return fmt.Errorf("error setting natOutgoing: %w", err)
	}
	if err := unstructured.SetNestedField(desired.Object, blockSize, "spec", "blockSize"); err != nil {
		return fmt.Errorf("error setting blockSize: %w", err)
	}
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
		logger.Info("creating Calico IPPool", "name", poolName)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("error creating Calico IPPool %s: %w", poolName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("error getting IPPool: %w", err)
	}

	existing.Object["spec"] = desired.Object["spec"]
	existing.SetLabels(desired.GetLabels())
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("error updating Calico IPPool %s: %w", poolName, err)
	}
	return nil
}

func (r *CoilReconciler) upsertCoilEgress(ctx context.Context, ob *nc.Outbound, prefixes []string, addresses *nc.AddressAllocation, logger logr.Logger) error {
	replicas := int64(1)
	if ob.Spec.Replicas != nil {
		replicas = int64(*ob.Spec.Replicas)
	}

	// Build pool annotations.
	annotations := map[string]string{}
	if addresses != nil && len(addresses.IPv4) > 0 {
		poolRef, _ := json.Marshal([]string{fmt.Sprintf("%s-v4", ob.Name)})
		annotations["cni.projectcalico.org/ipv4pools"] = string(poolRef)
	}
	if addresses != nil && len(addresses.IPv6) > 0 {
		poolRef, _ := json.Marshal([]string{fmt.Sprintf("%s-v6", ob.Name)})
		annotations["cni.projectcalico.org/ipv6pools"] = string(poolRef)
	}

	// Convert prefixes to []interface{} for unstructured.
	destSlice := make([]interface{}, len(prefixes))
	for i, p := range prefixes {
		destSlice[i] = p
	}

	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(coilEgressGVK)
	desired.SetName(ob.Name)
	desired.SetNamespace(ob.Namespace)
	desired.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by":                managedByValue,
		"network-connector.sylvaproject.org/outbound": ob.Name,
	})

	if err := unstructured.SetNestedSlice(desired.Object, destSlice, "spec", "destinations"); err != nil {
		return fmt.Errorf("error setting destinations: %w", err)
	}
	if err := unstructured.SetNestedField(desired.Object, replicas, "spec", "replicas"); err != nil {
		return fmt.Errorf("error setting replicas: %w", err)
	}
	if len(annotations) > 0 {
		if err := unstructured.SetNestedStringMap(desired.Object, annotations, "spec", "template", "metadata", "annotations"); err != nil {
			return fmt.Errorf("error setting template annotations: %w", err)
		}
	}

	// Add labels to pod template so the egress NetworkPolicy can select these pods.
	templateLabels := map[string]string{
		"network-connector.sylvaproject.org/outbound": ob.Name,
	}
	if err := unstructured.SetNestedStringMap(desired.Object, templateLabels, "spec", "template", "metadata", "labels"); err != nil {
		return fmt.Errorf("error setting template labels: %w", err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(coilEgressGVK)
	err := r.Get(ctx, types.NamespacedName{Name: ob.Name, Namespace: ob.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating Coil Egress", "name", ob.Name, "namespace", ob.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("error creating Coil Egress %s/%s: %w", ob.Namespace, ob.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("error getting Egress: %w", err)
	}

	existing.Object["spec"] = desired.Object["spec"]
	existing.SetLabels(desired.GetLabels())
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("error updating Coil Egress %s/%s: %w", ob.Namespace, ob.Name, err)
	}
	return nil
}

// upsertEgressNetworkPolicy creates or updates a NetworkPolicy restricting the
// Coil gateway pods to FoU tunnel traffic (UDP 5555) and destination prefixes.
// Infrastructure access (K8s API, DNS) is handled by cluster-level Calico
// GlobalNetworkPolicy + GlobalNetworkSet — see e2e/calico/ for E2E examples.
func (r *CoilReconciler) upsertEgressNetworkPolicy(ctx context.Context, ob *nc.Outbound, prefixes []string, logger logr.Logger) error {
	policyName := fmt.Sprintf("%s-egress", ob.Name)

	udp := corev1.ProtocolUDP
	fouPort := intstr.FromInt32(5555) //nolint:mnd // Coil FoU default port

	// Ingress: allow FoU from any source (tunnel return traffic).
	ingressRules := []networkingv1.NetworkPolicyIngressRule{{
		Ports: []networkingv1.NetworkPolicyPort{{
			Protocol: &udp,
			Port:     &fouPort,
		}},
	}}

	// Egress: allow FoU to any destination (tunnels terminate on node IPs).
	egressRules := []networkingv1.NetworkPolicyEgressRule{{
		Ports: []networkingv1.NetworkPolicyPort{{
			Protocol: &udp,
			Port:     &fouPort,
		}},
	}}

	// Egress: allow destination prefixes (external networks the gateway routes to).
	if len(prefixes) > 0 {
		egressPeers := make([]networkingv1.NetworkPolicyPeer, 0, len(prefixes))
		for _, prefix := range prefixes {
			egressPeers = append(egressPeers, networkingv1.NetworkPolicyPeer{
				IPBlock: &networkingv1.IPBlock{CIDR: prefix},
			})
		}
		egressRules = append(egressRules, networkingv1.NetworkPolicyEgressRule{
			To: egressPeers,
		})
	}

	desired := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: ob.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":                managedByValue,
				"network-connector.sylvaproject.org/outbound": ob.Name,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"network-connector.sylvaproject.org/outbound": ob.Name,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: ingressRules,
			Egress:  egressRules,
		},
	}

	existing := &networkingv1.NetworkPolicy{}
	getErr := r.APIReader.Get(ctx, types.NamespacedName{Name: policyName, Namespace: ob.Namespace}, existing)
	if apierrors.IsNotFound(getErr) {
		logger.Info("creating egress NetworkPolicy", "name", policyName)
		if createErr := r.Create(ctx, desired); createErr != nil {
			return fmt.Errorf("error creating NetworkPolicy %s: %w", policyName, createErr)
		}
		return nil
	}
	if getErr != nil {
		return fmt.Errorf("error getting NetworkPolicy: %w", getErr)
	}

	existing.Spec = desired.Spec
	existing.Labels = desired.Labels
	if updateErr := r.Update(ctx, existing); updateErr != nil {
		return fmt.Errorf("error updating NetworkPolicy %s: %w", policyName, updateErr)
	}
	return nil
}

func (r *CoilReconciler) handleOutboundDeletion(ctx context.Context, ob *nc.Outbound, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(ob, coilFinalizer) {
		return ctrl.Result{}, nil
	}

	// Delete Coil Egress.
	egress := &unstructured.Unstructured{}
	egress.SetGroupVersionKind(coilEgressGVK)
	egress.SetName(ob.Name)
	egress.SetNamespace(ob.Namespace)
	if err := r.Delete(ctx, egress); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("error deleting Coil Egress: %w", err)
	}
	logger.Info("deleted Coil Egress", "name", ob.Name)

	// Delete egress NetworkPolicy.
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-egress", ob.Name),
			Namespace: ob.Namespace,
		},
	}
	if err := r.Delete(ctx, np); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("error deleting egress NetworkPolicy: %w", err)
	}
	logger.Info("deleted egress NetworkPolicy", "name", np.Name)

	// Delete Calico IPPools.
	for _, suffix := range []string{"v4", "v6"} {
		pool := &unstructured.Unstructured{}
		pool.SetGroupVersionKind(calicoIPPoolGVK)
		pool.SetName(fmt.Sprintf("%s-%s", ob.Name, suffix))
		if err := r.Delete(ctx, pool); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("error deleting Calico IPPool %s: %w", pool.GetName(), err)
		}
		logger.Info("deleted Calico IPPool", "name", pool.GetName())
	}

	controllerutil.RemoveFinalizer(ob, coilFinalizer)
	if err := r.Update(ctx, ob); err != nil {
		return ctrl.Result{}, fmt.Errorf("error removing finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

// mapDestinationToOutbounds maps Destination changes to Outbound reconcile requests
// by listing all Outbounds and checking if their label selector matches the changed Destination.
func (r *CoilReconciler) mapDestinationToOutbounds(ctx context.Context, obj client.Object) []ctrlreconcile.Request {
	logger := log.FromContext(ctx)

	var outboundList nc.OutboundList
	if err := r.List(ctx, &outboundList); err != nil {
		logger.Error(err, "error listing outbounds for destination mapping")
		return nil
	}

	var requests []ctrlreconcile.Request
	for i := range outboundList.Items {
		ob := &outboundList.Items[i]
		if ob.Spec.Destinations == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(ob.Spec.Destinations)
		if err != nil {
			logger.Error(err, "error parsing destination selector", "outbound", ob.Name)
			continue
		}
		if selector.Matches(labels.Set(obj.GetLabels())) {
			requests = append(requests, ctrlreconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      ob.Name,
					Namespace: ob.Namespace,
				},
			})
		}
	}
	return requests
}

// SetupWithManager registers the Coil controller.
func (r *CoilReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("coil-reconciler").
		For(&nc.Outbound{}).
		Watches(&nc.Destination{}, handler.EnqueueRequestsFromMapFunc(r.mapDestinationToOutbounds)).
		Complete(r); err != nil {
		return fmt.Errorf("error setting up coil controller: %w", err)
	}
	return nil
}

// checkTargetCRDs verifies that Calico IPPool and Coil Egress CRDs are registered.
// Returns (true, _) if ready, or (false, requeueResult) if not.
func (r *CoilReconciler) checkTargetCRDs(ctx context.Context, logger logr.Logger) (bool, ctrl.Result) {
	for _, gvk := range []schema.GroupVersionKind{calicoIPPoolGVK, coilEgressGVK} {
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
