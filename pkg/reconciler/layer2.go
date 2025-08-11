package reconciler

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
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
		logger := r.Logger.WithValues("name", item.ObjectMeta.Name, "namespace", item.ObjectMeta.Namespace, "vlan", item.Spec.ID, "vni", item.Spec.VNI)
		if item.Spec.NodeSelector != nil {
			selector := labels.NewSelector()
			var reqs labels.Requirements

			for key, value := range item.Spec.NodeSelector.MatchLabels {
				requirement, err := labels.NewRequirement(key, selection.Equals, []string{value})
				if err != nil {
					logger.Error(err, "error creating MatchLabel requirement")
					return nil, fmt.Errorf("error creating MatchLabel requirement: %w", err)
				}
				reqs = append(reqs, *requirement)
			}

			for _, req := range item.Spec.NodeSelector.MatchExpressions {
				lowercaseOperator := selection.Operator(strings.ToLower(string(req.Operator)))
				requirement, err := labels.NewRequirement(req.Key, lowercaseOperator, req.Values)
				if err != nil {
					logger.Error(err, "error creating MatchExpression requirement")
					return nil, fmt.Errorf("error creating MatchExpression requirement: %w", err)
				}
				reqs = append(reqs, *requirement)
			}
			selector = selector.Add(reqs...)

			if !selector.Matches(labels.Set(node.ObjectMeta.Labels)) {
				logger.Info("local node does not match nodeSelector of layer2", "node", nodeName)
				continue
			}
		}

		l2vnis = append(l2vnis, *item)
	}

	if err := r.checkL2Duplicates(l2vnis); err != nil {
		return nil, err
	}

	return l2vnis, nil
}

func (r *reconcile) reconcileLayer2(data *reconcileData) error {
	desired, err := r.getDesired(data.l2vnis)
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

	if err := r.netlinkManager.ReconcileL2NodeConfig(); err != nil {
		r.Logger.Error(err, "error reconciling L2 node config")
	}

	return nil
}

func (r *reconcile) createL2(info *nl.Layer2Information, anycastTrackerInterfaces *[]int) error {
	r.Logger.Info("Creating Layer2", "vlan", info.VlanID, "vni", info.VNI)
	err := r.netlinkManager.CreateL2(info)
	if err != nil {
		return fmt.Errorf("error creating layer2 vlan %d vni %d: %w", info.VlanID, info.VNI, err)
	}
	return r.applyConfiguration(info, anycastTrackerInterfaces)
}

func (r *reconcile) getDesired(l2vnis []networkv1alpha1.Layer2NetworkConfiguration) ([]nl.Layer2Information, error) {
	availableVrfs, err := r.netlinkManager.ListL3()
	if err != nil {
		return nil, fmt.Errorf("error loading available VRFs: %w", err)
	}

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

		if spec.VRF != "" {
			vrfAvailable := false
			for _, info := range availableVrfs {
				if info.Name == spec.VRF {
					vrfAvailable = true
					break
				}
			}
			if !vrfAvailable {
				r.Logger.Error(err, "VRF of Layer2 not found on node", "layer", l2vnis[i].ObjectMeta.Name, "vrf", spec.VRF)
				continue
			}
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
			CreateMACVLANInterface: true, // Create MACVLAN interface by default
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
	return r.applyConfiguration(desired, anycastTrackerInterfaces)
}

func (r *reconcile) applyConfiguration(info *nl.Layer2Information, anycastTrackerInterfaces *[]int) error {
	if info.AdvertiseNeighbors {
		bridgeID := info.BridgeID()
		if bridgeID == -1 {
			return fmt.Errorf("error getting bridge id for vlanId %d", info.VlanID)
		}
		*anycastTrackerInterfaces = append(*anycastTrackerInterfaces, bridgeID)
	}

	if info.MacVLANBridgeID() != -1 {
		if info.IsNeighSuppressionEnabled() {
			if err := r.neighborSync.EnsureNeighborSuppression(info.BridgeID(), info.MacVLANBridgeID()); err != nil {
				return fmt.Errorf("error ensuring neighbor suppression for vlanId %d: %w", info.VlanID, err)
			}
		} else {
			r.neighborSync.DisableNeighborSuppression(info.BridgeID(), info.MacVLANBridgeID())
		}
	}

	if len(info.AnycastGateways) > 0 && info.BridgeID() != -1 {
		r.neighborSync.EnsureARPRefresh(info.BridgeID())
	} else {
		r.neighborSync.DisableARPRefresh(info.BridgeID())
	}
	return nil
}

func (*reconcile) checkL2Duplicates(configs []networkv1alpha1.Layer2NetworkConfiguration) error {
	for i := range configs {
		for j := i + 1; j < len(configs); j++ {
			if configs[i].Spec.ID == configs[j].Spec.ID {
				return fmt.Errorf("dupliate Layer2 ID found: %s %s", configs[i].ObjectMeta.Name, configs[j].ObjectMeta.Name)
			}
			if configs[i].Spec.VNI == configs[j].Spec.VNI {
				return fmt.Errorf("dupliate Layer2 VNI found: %s %s", configs[i].ObjectMeta.Name, configs[j].ObjectMeta.Name)
			}
		}
	}
	return nil
}
