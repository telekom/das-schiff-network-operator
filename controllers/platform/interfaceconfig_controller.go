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

	"github.com/go-logr/logr"
	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
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
type InterfaceConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=interfaceconfigs,verbs=get;list;watch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=interfaceconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=interfaceconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=network.t-caas.telekom.com,resources=nodenetplanconfigs,verbs=get;list;watch;create;update;patch;delete

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

	nodes, err := r.matchingNodes(ctx, ifconfig)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error matching nodes: %w", err)
	}

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

// reconcileNodeConfig creates or updates a NodeNetplanConfig for a specific node.
// The agent watches NodeNetplanConfigs by node name and applies the desired state.
func (r *InterfaceConfigReconciler) reconcileNodeConfig(ctx context.Context, ifconfig *nc.InterfaceConfig, node *corev1.Node, logger logr.Logger) error {
	ethernets, err := buildNetplanEthernets(ifconfig.Spec.Ethernets)
	if err != nil {
		return fmt.Errorf("building netplan ethernets: %w", err)
	}
	bonds, err := buildNetplanBonds(ifconfig.Spec.Bonds)
	if err != nil {
		return fmt.Errorf("building netplan bonds: %w", err)
	}

	desired := &networkv1alpha1.NodeNetplanConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":           "network-connector",
				"network-connector.sylvaproject.org/type": "nodenetplanconfig",
			},
		},
		Spec: networkv1alpha1.NodeNetplanConfigSpec{
			DesiredState: netplan.State{
				Network: netplan.NetworkState{
					Version:   2, //nolint:mnd // netplan specification version
					Ethernets: ethernets,
					Bonds:     bonds,
				},
			},
		},
	}

	existing := &networkv1alpha1.NodeNetplanConfig{}
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating NodeNetplanConfig", "node", node.Name)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("error creating NodeNetplanConfig for node %s: %w", node.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("error getting NodeNetplanConfig: %w", err)
	}

	existing.Spec = desired.Spec
	existing.Labels = desired.Labels
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("error updating NodeNetplanConfig for node %s: %w", node.Name, err)
	}
	return nil
}

func (r *InterfaceConfigReconciler) handleDeletion(ctx context.Context, ifconfig *nc.InterfaceConfig, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(ifconfig, ifconfigFinalizer) {
		return ctrl.Result{}, nil
	}

	// Delete NodeNetplanConfigs for matching nodes.
	nodes, err := r.matchingNodes(ctx, ifconfig)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error matching nodes: %w", err)
	}

	for i := range nodes {
		nnpc := &networkv1alpha1.NodeNetplanConfig{
			ObjectMeta: metav1.ObjectMeta{Name: nodes[i].Name},
		}
		if err := r.Delete(ctx, nnpc); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("error deleting NodeNetplanConfig %s: %w", nodes[i].Name, err)
		}
		logger.Info("deleted NodeNetplanConfig", "node", nodes[i].Name)
	}

	controllerutil.RemoveFinalizer(ifconfig, ifconfigFinalizer)
	return ctrl.Result{}, r.Update(ctx, ifconfig)
}

// buildNetplanEthernets converts InterfaceConfig ethernets to netplan Device map.
func buildNetplanEthernets(ethernets map[string]nc.EthernetConfig) (map[string]netplan.Device, error) {
	if len(ethernets) == 0 {
		return nil, nil
	}

	result := make(map[string]netplan.Device, len(ethernets))
	for name, cfg := range ethernets {
		dev := make(map[string]interface{})
		if cfg.Mtu != nil {
			dev["mtu"] = *cfg.Mtu
		}
		if cfg.VirtualFunctionCount != nil {
			dev["virtual-function-count"] = *cfg.VirtualFunctionCount
		}

		raw, err := json.Marshal(dev)
		if err != nil {
			return nil, fmt.Errorf("marshaling ethernet %q: %w", name, err)
		}
		result[name] = netplan.Device{Raw: raw}
	}
	return result, nil
}

// buildNetplanBonds converts InterfaceConfig bonds to netplan Device map.
func buildNetplanBonds(bonds map[string]nc.BondConfig) (map[string]netplan.Device, error) {
	if len(bonds) == 0 {
		return nil, nil
	}

	result := make(map[string]netplan.Device, len(bonds))
	for name, cfg := range bonds {
		dev := map[string]interface{}{
			"interfaces": cfg.Interfaces,
		}
		if cfg.Mtu != nil {
			dev["mtu"] = *cfg.Mtu
		}
		if cfg.Parameters != nil {
			params := map[string]interface{}{
				"mode": cfg.Parameters.Mode,
			}
			if cfg.Parameters.MiiMonitorInterval != nil {
				params["mii-monitor-interval"] = *cfg.Parameters.MiiMonitorInterval
			}
			if cfg.Parameters.LacpRate != nil {
				params["lacp-rate"] = *cfg.Parameters.LacpRate
			}
			if cfg.Parameters.UpDelay != nil {
				params["up-delay"] = *cfg.Parameters.UpDelay
			}
			if cfg.Parameters.DownDelay != nil {
				params["down-delay"] = *cfg.Parameters.DownDelay
			}
			if cfg.Parameters.TransmitHashPolicy != nil {
				params["transmit-hash-policy"] = *cfg.Parameters.TransmitHashPolicy
			}
			dev["parameters"] = params
		}

		raw, err := json.Marshal(dev)
		if err != nil {
			return nil, fmt.Errorf("marshaling bond %q: %w", name, err)
		}
		result[name] = netplan.Device{Raw: raw}
	}
	return result, nil
}

// SetupWithManager registers the InterfaceConfig controller.
func (r *InterfaceConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("interfaceconfig-reconciler").
		For(&nc.InterfaceConfig{}).
		Watches(&corev1.Node{}, &handler.EnqueueRequestForObject{}).
		Complete(r); err != nil {
		return fmt.Errorf("error setting up interfaceconfig controller: %w", err)
	}
	return nil
}
