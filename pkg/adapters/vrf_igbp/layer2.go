package adapters

import (
	"fmt"
	"net"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

func (r *VrfIgbp) ReconcileLayer2(l2vnis []v1alpha1.Layer2NetworkConfigurationSpec) error {
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
		r.logger.Info("Deleting Layer2 because it is no longer configured", "vlan", toDelete[i].VlanID, "vni", toDelete[i].VNI)
		errs := r.netlinkManager.CleanupL2(&toDelete[i])
		for _, err := range errs {
			r.logger.Error(err, "Error deleting Layer2", "vlan", toDelete[i].VlanID, "vni", toDelete[i].VNI)
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

func (r *VrfIgbp) createL2(info *nl.Layer2Information, anycastTrackerInterfaces *[]int) error {
	r.logger.Info("Creating Layer2", "vlan", info.VlanID, "vni", info.VNI)
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

func (r *VrfIgbp) getDesired(l2vnis []v1alpha1.Layer2NetworkConfigurationSpec) ([]nl.Layer2Information, error) {
	availableVrfs, err := r.netlinkManager.ListL3()
	if err != nil {
		return nil, fmt.Errorf("error loading available VRFs: %w", err)
	}

	desired := []nl.Layer2Information{}
	for i := range l2vnis {
		spec := l2vnis[i]

		var anycastMAC *net.HardwareAddr
		if mac, err := net.ParseMAC(spec.AnycastMac); err == nil {
			anycastMAC = &mac
		}

		anycastGateways, err := r.netlinkManager.ParseIPAddresses(spec.AnycastGateways)
		if err != nil {
			r.logger.Error(err, "error parsing anycast gateways", "gw", spec.AnycastGateways)
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
				r.logger.Error(err, "VRF of Layer2 not found on node", "vrf", spec.VRF)
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

func (r *VrfIgbp) reconcileExistingLayer(desired, currentConfig *nl.Layer2Information, anycastTrackerInterfaces *[]int) error {
	r.logger.Info("Reconciling existing Layer2", "vlan", desired.VlanID, "vni", desired.VNI)
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
