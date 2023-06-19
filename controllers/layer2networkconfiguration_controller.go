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
	"net"
	"os"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Layer2NetworkConfigurationReconciler reconciles a Layer2NetworkConfiguration object
type Layer2NetworkConfigurationReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	AnycastTracker *anycast.AnycastTracker
	Debouncer      *debounce.Debouncer
	NLManager      *nl.NetlinkManager
}

//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=layer2networkconfigurations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=layer2networkconfigurations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=layer2networkconfigurations/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Layer2NetworkConfiguration object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *Layer2NetworkConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Run ReconcileDebounced through debouncer
	r.Debouncer.Debounce(ctx)

	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Layer2NetworkConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Debouncer = debounce.NewDebouncer(r.ReconcileDebounced, 5*time.Second)
	r.NLManager = &nl.NetlinkManager{}

	// Create empty request for changes to node
	nodesMapFn := handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request { return []reconcile.Request{{}} })
	nodePredicates := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return os.Getenv("HOSTNAME") == e.ObjectNew.GetName() && !cmp.Equal(e.ObjectNew.GetLabels(), e.ObjectOld.GetLabels())
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1alpha1.Layer2NetworkConfiguration{}).
		Watches(&source.Kind{Type: &corev1.Node{}}, nodesMapFn, builder.WithPredicates(nodePredicates)).
		Complete(r)
}

func (r *Layer2NetworkConfigurationReconciler) ReconcileDebounced(ctx context.Context) error {
	logger := log.FromContext(ctx)

	layer2s := &networkv1alpha1.Layer2NetworkConfigurationList{}
	err := r.Client.List(ctx, layer2s)
	if err != nil {
		logger.Error(err, "error getting list of Layer2s from Kubernetes")
		return err
	}

	nodeName := os.Getenv("HOSTNAME")
	node := &corev1.Node{}
	err = r.Client.Get(ctx, types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		logger.Error(err, "error getting local node name")
		return err
	}

	layer2Info := []nl.Layer2Information{}

	for _, layer2 := range layer2s.Items {
		spec := layer2.Spec

		if spec.NodeSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(spec.NodeSelector)
			if err != nil {
				logger.Error(err, "error converting nodeSelector of layer2 to selector", "layer2", layer2.ObjectMeta.Name)
				return err
			}
			if !selector.Matches(labels.Set(node.ObjectMeta.Labels)) {
				logger.Info("local node does not match nodeSelector of layer2", "layer2", layer2.ObjectMeta.Name, "node", nodeName)
				return err
			}
		}

		var anycastMAC *net.HardwareAddr
		if mac, err := net.ParseMAC(spec.AnycastMac); err == nil {
			anycastMAC = &mac
		}

		anycastGateways, err := r.NLManager.ParseIPAddresses(spec.AnycastGateways)
		if err != nil {
			logger.Error(err, "error parsing anycast gateways", "layer", layer2.ObjectMeta.Name, "gw", spec.AnycastGateways)
			return err
		}

		layer2Info = append(layer2Info, nl.Layer2Information{
			VlanID:                 spec.ID,
			MTU:                    spec.MTU,
			VNI:                    spec.VNI,
			VRF:                    spec.VRF,
			AnycastMAC:             anycastMAC,
			AnycastGateways:        anycastGateways,
			AdvertiseNeighbors:     spec.AdvertiseNeighbors,
			CreateMACVLANInterface: spec.CreateMACVLANInterface,
		})
	}

	if err := r.reconcileLayer2(ctx, layer2Info); err != nil {
		logger.Error(err, "error reconciling Layer2s")
		return err
	}

	return nil
}

func (r *Layer2NetworkConfigurationReconciler) reconcileLayer2(ctx context.Context, layer2Info []nl.Layer2Information) error {
	logger := log.FromContext(ctx)

	existing, err := r.NLManager.ListL2()
	if err != nil {
		return err
	}

	delete := []nl.Layer2Information{}
	for _, cfg := range existing {
		stillExists := false
		for _, info := range layer2Info {
			if info.VlanID == cfg.VlanID {
				// Maybe reconcile to match MTU, gateways?
				stillExists = true
			}
		}
		if !stillExists {
			delete = append(delete, cfg)
		}
	}

	create := []nl.Layer2Information{}
	anycastTrackerInterfaces := []int{}
	for _, info := range layer2Info {
		alreadyExists := false
		var currentConfig nl.Layer2Information
		for _, cfg := range existing {
			if info.VlanID == cfg.VlanID {
				alreadyExists = true
				currentConfig = cfg
			}
		}
		if !alreadyExists {
			create = append(create, info)
		} else {
			err := r.NLManager.ReconcileL2(currentConfig, info)
			if err != nil {
				return fmt.Errorf("error reconciling layer2 vlan %d vni %d: %v", info.VlanID, info.VNI, err)
			}
			if info.AdvertiseNeighbors {
				bridgeId, err := r.NLManager.GetBridgeId(info)
				if err != nil {
					return fmt.Errorf("error getting bridge id for vlanId %d: %v", info.VlanID, err)
				}
				anycastTrackerInterfaces = append(anycastTrackerInterfaces, bridgeId)
			}
		}
	}

	for _, info := range delete {
		logger.Info("Deleting Layer2 because it is no longer configured", "vlan", info.VlanID, "vni", info.VNI)
		errs := r.NLManager.CleanupL2(info)
		for _, err := range errs {
			logger.Error(err, "Error deleting Layer2", "vlan", info.VlanID, "vni", info.VNI)
		}
	}

	for _, info := range create {
		logger.Info("Creating Layer2", "vlan", info.VlanID, "vni", info.VNI)
		err := r.NLManager.CreateL2(info)
		if err != nil {
			return fmt.Errorf("error creating layer2 vlan %d vni %d: %v", info.VlanID, info.VNI, err)
		}
		if info.AdvertiseNeighbors {
			bridgeId, err := r.NLManager.GetBridgeId(info)
			if err != nil {
				return fmt.Errorf("error getting bridge id for vlanId %d: %v", info.VlanID, err)
			}
			anycastTrackerInterfaces = append(anycastTrackerInterfaces, bridgeId)
		}
	}

	r.AnycastTracker.TrackedBridges = anycastTrackerInterfaces

	return nil
}
