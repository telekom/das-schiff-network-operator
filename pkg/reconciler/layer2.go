package reconciler

import (
	"context"
	"fmt"
	"net"
	"os"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

func (r *reconcile) fetchLayer2(ctx context.Context) ([]networkv1alpha1.Layer2NetworkConfiguration, error) {
	layer2List := &networkv1alpha1.Layer2NetworkConfigurationList{}
	err := r.client.List(ctx, layer2List)
	if err != nil {
		r.Logger.Error(err, "error getting list of Layer2s from Kubernetes")
		return nil, fmt.Errorf("error getting list of Layer2s from Kubernetes: %w", err)
	}

	nodeName := os.Getenv(healthcheck.NodenameEnv)
	node := &corev1.Node{}
	err = r.client.Get(ctx, types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		r.Logger.Error(err, "error getting local node name")
		return nil, fmt.Errorf("error getting local node name: %w", err)
	}

	l2vnis := []networkv1alpha1.Layer2NetworkConfiguration{}

	for i := range layer2List.Items {
		item := &layer2List.Items[i]
		if item.Spec.NodeSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(item.Spec.NodeSelector)
			if err != nil {
				r.Logger.Error(err, "error converting nodeSelector of layer2 to selector", "layer2", item.ObjectMeta.Name)
				return nil, fmt.Errorf("error converting nodeSelector of layer2 to selector: %w", err)
			}
			if !selector.Matches(labels.Set(node.ObjectMeta.Labels)) {
				r.Logger.Info("local node does not match nodeSelector of layer2", "layer2", item.ObjectMeta.Name, "node", nodeName)
				continue
			}
		}

		l2vnis = append(l2vnis, *item)
	}

	return l2vnis, nil
}

func (r *reconcile) reconcileLayer2(l2vnis []networkv1alpha1.Layer2NetworkConfiguration) error {
	desired, err := r.getDesired(l2vnis)
	if err != nil {
		return err
	}

	existing, err := r.netlinkManager.ListL2()
	if err != nil {
		return fmt.Errorf("error listing L2: %w", err)
	}

	toDelete := determineToBeDeleted(existing, desired)

	create := []nl.Layer2Information{}
	anycastTrackerInterfaces := []int{}
	for i := range desired {
		alreadyExists := false
		var currentConfig nl.Layer2Information
		for j := range existing {
			if desired[i].VlanID == existing[j].VlanID {
				alreadyExists = true
				currentConfig = existing[j]
				break
			}
		}
		if !alreadyExists {
			create = append(create, desired[i])
		} else {
			if err := r.reconcileExistingLayer(&desired[i], &currentConfig, &anycastTrackerInterfaces); err != nil {
				return err
			}
		}
	}

	for i := range toDelete {
		r.Logger.Info("Deleting Layer2 because it is no longer configured", "vlan", toDelete[i].VlanID, "vni", toDelete[i].VNI)
		errs := r.netlinkManager.CleanupL2(&toDelete[i])
		for _, err := range errs {
			r.Logger.Error(err, "Error deleting Layer2", "vlan", toDelete[i].VlanID, "vni", toDelete[i].VNI)
		}
	}

	for i := range create {
		if err := r.createL2(&create[i], &anycastTrackerInterfaces); err != nil {
			return err
		}
	}

	r.anycastTracker.TrackedBridges = anycastTrackerInterfaces

	return nil
}

func (r *reconcile) createL2(info *nl.Layer2Information, anycastTrackerInterfaces *[]int) error {
	r.Logger.Info("Creating Layer2", "vlan", info.VlanID, "vni", info.VNI)
	err := r.netlinkManager.CreateL2(info)
	if err != nil {
		return fmt.Errorf("error creating layer2 vlan %d vni %d: %w", info.VlanID, info.VNI, err)
	}
	if info.AdvertiseNeighbors {
		bridgeID, err := r.netlinkManager.GetBridgeID(info)
		if err != nil {
			return fmt.Errorf("error getting bridge id for vlanId %d: %w", info.VlanID, err)
		}
		*anycastTrackerInterfaces = append(*anycastTrackerInterfaces, bridgeID)
	}
	return nil
}

func (r *reconcile) getDesired(l2vnis []networkv1alpha1.Layer2NetworkConfiguration) ([]nl.Layer2Information, error) {
	desired := []nl.Layer2Information{}
	for i := range l2vnis {
		spec := l2vnis[i].Spec

		var anycastMAC *net.HardwareAddr
		if mac, err := net.ParseMAC(spec.AnycastMac); err == nil {
			anycastMAC = &mac
		}

		anycastGateways, err := r.netlinkManager.ParseIPAddresses(spec.AnycastGateways)
		if err != nil {
			r.Logger.Error(err, "error parsing anycast gateways", "layer", l2vnis[i].ObjectMeta.Name, "gw", spec.AnycastGateways)
			return nil, fmt.Errorf("error parsing anycast gateways: %w", err)
		}

		desired = append(desired, nl.Layer2Information{
			VlanID:                 spec.ID,
			MTU:                    spec.MTU,
			VNI:                    spec.VNI,
			VRF:                    spec.VRF,
			AnycastMAC:             anycastMAC,
			AnycastGateways:        anycastGateways,
			AdvertiseNeighbors:     spec.AdvertiseNeighbors,
			NeighSuppression:       spec.NeighSuppression,
			CreateMACVLANInterface: spec.CreateMACVLANInterface,
		})
	}

	return desired, nil
}

func determineToBeDeleted(existing, desired []nl.Layer2Information) []nl.Layer2Information {
	toDelete := []nl.Layer2Information{}
	for i := range existing {
		stillExists := false
		for j := range desired {
			if desired[j].VlanID == existing[i].VlanID {
				stillExists = true
				break
			}
		}
		if !stillExists {
			toDelete = append(toDelete, existing[i])
		}
	}
	return toDelete
}

func (r *reconcile) reconcileExistingLayer(desired, currentConfig *nl.Layer2Information, anycastTrackerInterfaces *[]int) error {
	r.Logger.Info("Reconciling existing Layer2", "vlan", desired.VlanID, "vni", desired.VNI)
	err := r.netlinkManager.ReconcileL2(currentConfig, desired)
	if err != nil {
		return fmt.Errorf("error reconciling layer2 vlan %d vni %d: %w", desired.VlanID, desired.VNI, err)
	}
	if desired.AdvertiseNeighbors {
		bridgeID, err := r.netlinkManager.GetBridgeID(desired)
		if err != nil {
			return fmt.Errorf("error getting bridge id for vlanId %d: %w", desired.VlanID, err)
		}
		*anycastTrackerInterfaces = append(*anycastTrackerInterfaces, bridgeID)
	}
	return nil
}
