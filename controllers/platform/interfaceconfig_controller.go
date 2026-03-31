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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	ifconfigFinalizer = "network-connector.sylvaproject.org/ifconfig-cleanup"
)

// InterfaceConfigReconciler watches InterfaceConfig and Node resources,
// producing per-node NodeNetplanConfig resources for the netplan agent.
//
// This is a scaffold — NodeNetplanConfig types already exist in the operator
// (api/v1alpha1). The reconciler creates per-node configs matching
// the InterfaceConfig's nodeSelector.
type InterfaceConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=interfaceconfigs,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=interfaceconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=interfaceconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile handles InterfaceConfig create/update/delete events.
func (r *InterfaceConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	ifconfig := &nc.InterfaceConfig{}
	if err := r.Get(ctx, req.NamespacedName, ifconfig); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("error fetching InterfaceConfig: %w", err)
	}

	if !ifconfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, ifconfig, logger)
	}

	if !controllerutil.ContainsFinalizer(ifconfig, ifconfigFinalizer) {
		controllerutil.AddFinalizer(ifconfig, ifconfigFinalizer)
		if err := r.Update(ctx, ifconfig); err != nil {
			return ctrl.Result{}, fmt.Errorf("error adding finalizer: %w", err)
		}
	}

	// List matching nodes.
	nodes, err := r.matchingNodes(ctx, ifconfig)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error matching nodes: %w", err)
	}

	// For each matching node, create/update a ConfigMap placeholder
	// for the netplan config. When NodeNetplanConfig types are wired,
	// this will produce proper typed resources.
	for i := range nodes {
		if err := r.reconcileNodeConfig(ctx, ifconfig, &nodes[i], logger); err != nil {
			logger.Error(err, "error reconciling node config", "node", nodes[i].Name)
		}
	}

	return ctrl.Result{}, nil
}

func (r *InterfaceConfigReconciler) matchingNodes(ctx context.Context, ifconfig *nc.InterfaceConfig) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("error listing nodes: %w", err)
	}

	if ifconfig.Spec.NodeSelector.MatchLabels == nil && len(ifconfig.Spec.NodeSelector.MatchExpressions) == 0 {
		return nodeList.Items, nil
	}

	sel, err := metav1.LabelSelectorAsSelector(&ifconfig.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid nodeSelector: %w", err)
	}

	var matched []corev1.Node
	for i := range nodeList.Items {
		if sel.Matches(labels.Set(nodeList.Items[i].Labels)) {
			matched = append(matched, nodeList.Items[i])
		}
	}

	return matched, nil
}

func (r *InterfaceConfigReconciler) reconcileNodeConfig(ctx context.Context, ifconfig *nc.InterfaceConfig, node *corev1.Node, logger logr.Logger) error {
	cmName := fmt.Sprintf("netplan-%s-%s", ifconfig.Name, node.Name)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: ifconfig.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":           "network-connector",
				"network-connector.sylvaproject.org/type": "nodenetplanconfig",
				"network-connector.sylvaproject.org/node": node.Name,
			},
		},
		Data: map[string]string{
			"interfaceConfig": ifconfig.Name,
			"node":            node.Name,
		},
	}

	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKeyFromObject(cm), existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating NodeNetplanConfig placeholder", "name", cmName, "node", node.Name)
		return r.Create(ctx, cm)
	}
	if err != nil {
		return fmt.Errorf("error getting ConfigMap: %w", err)
	}

	existing.Data = cm.Data
	existing.Labels = cm.Labels
	return r.Update(ctx, existing)
}

func (r *InterfaceConfigReconciler) handleDeletion(ctx context.Context, ifconfig *nc.InterfaceConfig, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(ifconfig, ifconfigFinalizer) {
		return ctrl.Result{}, nil
	}

	// Delete all placeholder ConfigMaps for this InterfaceConfig.
	cmList := &corev1.ConfigMapList{}
	if err := r.List(ctx, cmList, client.InNamespace(ifconfig.Namespace),
		client.MatchingLabels{
			"network-connector.sylvaproject.org/type": "nodenetplanconfig",
			"app.kubernetes.io/managed-by":            "network-connector",
		}); err != nil {
		return ctrl.Result{}, fmt.Errorf("error listing ConfigMaps: %w", err)
	}

	for i := range cmList.Items {
		if cmList.Items[i].Data["interfaceConfig"] == ifconfig.Name {
			if err := r.Delete(ctx, &cmList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("error deleting ConfigMap %s: %w", cmList.Items[i].Name, err)
			}
			logger.Info("deleted NodeNetplanConfig placeholder", "name", cmList.Items[i].Name)
		}
	}

	controllerutil.RemoveFinalizer(ifconfig, ifconfigFinalizer)
	return ctrl.Result{}, r.Update(ctx, ifconfig)
}

// SetupWithManager registers the InterfaceConfig controller.
func (r *InterfaceConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("interfaceconfig-reconciler").
		For(&nc.InterfaceConfig{}).
		Watches(&corev1.Node{}, &handler.EnqueueRequestForObject{}).
		Complete(r)
}
