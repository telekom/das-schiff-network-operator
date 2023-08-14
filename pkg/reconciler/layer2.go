package reconciler

import (
	"fmt"
	"net"
	"os"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *reconcile) fetchLayer2() ([]networkv1alpha1.Layer2NetworkConfiguration, error) {
	layer2List := &networkv1alpha1.Layer2NetworkConfigurationList{}
	err := r.client.List(r.Context, layer2List)
	if err != nil {
		r.Logger.Error(err, "error getting list of Layer2s from Kubernetes")
		return nil, err
	}

	nodeName := os.Getenv("HOSTNAME")
	node := &corev1.Node{}
	err = r.client.Get(r.Context, types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		r.Logger.Error(err, "error getting local node name")
		return nil, err
	}

	l2vnis := []networkv1alpha1.Layer2NetworkConfiguration{}

	for _, item := range layer2List.Items {
		if item.Spec.NodeSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(item.Spec.NodeSelector)
			if err != nil {
				r.Logger.Error(err, "error converting nodeSelector of layer2 to selector", "layer2", item.ObjectMeta.Name)
				return nil, err
			}
			if !selector.Matches(labels.Set(node.ObjectMeta.Labels)) {
				r.Logger.Info("local node does not match nodeSelector of layer2", "layer2", item.ObjectMeta.Name, "node", nodeName)
				continue
			}
		}

		l2vnis = append(l2vnis, item)
	}

	return l2vnis, nil
}

func (r *reconcile) reconcileLayer2(l2vnis []networkv1alpha1.Layer2NetworkConfiguration) error {
	desired := []nl.Layer2Information{}

	for _, layer2 := range l2vnis {
		spec := layer2.Spec

		var anycastMAC *net.HardwareAddr
		if mac, err := net.ParseMAC(spec.AnycastMac); err == nil {
			anycastMAC = &mac
		}

		anycastGateways, err := r.netlinkManager.ParseIPAddresses(spec.AnycastGateways)
		if err != nil {
			r.Logger.Error(err, "error parsing anycast gateways", "layer", layer2.ObjectMeta.Name, "gw", spec.AnycastGateways)
			return err
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

	existing, err := r.netlinkManager.ListL2()
	if err != nil {
		return err
	}

	toDelete := []nl.Layer2Information{}
	for _, cfg := range existing {
		stillExists := false
		for _, info := range desired {
			if info.VlanID == cfg.VlanID {
				stillExists = true
				break
			}
		}
		if !stillExists {
			toDelete = append(toDelete, cfg)
		}
	}

	create := []nl.Layer2Information{}
	anycastTrackerInterfaces := []int{}
	for _, info := range desired {
		alreadyExists := false
		var currentConfig nl.Layer2Information
		for _, cfg := range existing {
			if info.VlanID == cfg.VlanID {
				alreadyExists = true
				currentConfig = cfg
				break
			}
		}
		if !alreadyExists {
			create = append(create, info)
		} else {
			r.Logger.Info("Reconciling existing Layer2", "vlan", info.VlanID, "vni", info.VNI)
			err := r.netlinkManager.ReconcileL2(&currentConfig, &info)
			if err != nil {
				return fmt.Errorf("error reconciling layer2 vlan %d vni %d: %w", info.VlanID, info.VNI, err)
			}
			if info.AdvertiseNeighbors {
				bridgeId, err := r.netlinkManager.GetBridgeID(&info)
				if err != nil {
					return fmt.Errorf("error getting bridge id for vlanId %d: %w", info.VlanID, err)
				}
				anycastTrackerInterfaces = append(anycastTrackerInterfaces, bridgeId)
			}
		}
	}

	for _, info := range toDelete {
		r.Logger.Info("Deleting Layer2 because it is no longer configured", "vlan", info.VlanID, "vni", info.VNI)
		errs := r.netlinkManager.CleanupL2(&info)
		for _, err := range errs {
			r.Logger.Error(err, "Error deleting Layer2", "vlan", info.VlanID, "vni", info.VNI)
		}
	}

	for _, info := range create {
		r.Logger.Info("Creating Layer2", "vlan", info.VlanID, "vni", info.VNI)
		err := r.netlinkManager.CreateL2(&info)
		if err != nil {
			return fmt.Errorf("error creating layer2 vlan %d vni %d: %w", info.VlanID, info.VNI, err)
		}
		if info.AdvertiseNeighbors {
			bridgeId, err := r.netlinkManager.GetBridgeID(&info)
			if err != nil {
				return fmt.Errorf("error getting bridge id for vlanId %d: %w", info.VlanID, err)
			}
			anycastTrackerInterfaces = append(anycastTrackerInterfaces, bridgeId)
		}
	}

	r.anycastTracker.TrackedBridges = anycastTrackerInterfaces

	return nil
}
