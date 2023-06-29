package reconciler

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

func (r *reconcile) fetchLayer3() ([]networkv1alpha1.VRFRouteConfiguration, error) {
	vrfs := &networkv1alpha1.VRFRouteConfigurationList{}
	err := r.client.List(r.Context, vrfs)
	if err != nil {
		r.Logger.Error(err, "error getting list of VRFs from Kubernetes")
		return nil, err
	}

	return vrfs.Items, nil
}

func (r *reconcile) reconcileLayer3(l3vnis []networkv1alpha1.VRFRouteConfiguration) error {
	vrfConfigMap := map[string]frr.VRFConfiguration{}

	for _, vrf := range l3vnis {
		spec := vrf.Spec

		var vni int
		var rt string

		if val, ok := r.config.VRFConfig[spec.VRF]; ok {
			vni = val.VNI
			rt = val.RT
			r.Logger.Info("Configuring VRF from new VRFConfig", "vrf", spec.VRF, "vni", val.VNI, "rt", rt)
		} else if val, ok := r.config.VRFToVNI[spec.VRF]; ok {
			vni = val
			r.Logger.Info("Configuring VRF from old VRFToVNI", "vrf", spec.VRF, "vni", val)
		} else if r.config.ShouldSkipVRFConfig(spec.VRF) {
			vni = config.SKIP_VRF_TEMPLATE_VNI
		} else {
			err := fmt.Errorf("vrf not in vrf vni map")
			r.Logger.Error(err, "VRF does not exist in VRF VNI config", "vrf", spec.VRF, "name", vrf.ObjectMeta.Name, "namespace", vrf.ObjectMeta.Namespace)
			return err
		}

		// If VRF is not yet in dict, initialize it
		if _, ok := vrfConfigMap[spec.VRF]; !ok {
			vrfConfigMap[spec.VRF] = frr.VRFConfiguration{
				Name: spec.VRF,
				VNI:  vni,
				RT:   rt,
			}
		}

		config := vrfConfigMap[spec.VRF]

		if len(spec.Export) > 0 {
			prefixList, err := handlePrefixItemList(spec.Export, spec.Seq)
			if err != nil {
				return err
			}
			config.Export = append(config.Export, prefixList)
		}
		if len(spec.Import) > 0 {
			prefixList, err := handlePrefixItemList(spec.Import, spec.Seq)
			if err != nil {
				return err
			}
			config.Import = append(config.Import, prefixList)
		}
		for _, aggregate := range spec.Aggregate {
			_, network, err := net.ParseCIDR(aggregate)
			if err != nil {
				return err
			}
			if network.IP.To4() == nil {
				config.AggregateIPv6 = append(config.AggregateIPv6, aggregate)
			} else {
				config.AggregateIPv4 = append(config.AggregateIPv4, aggregate)
			}
		}
		vrfConfigMap[spec.VRF] = config
	}

	vrfConfigs := []frr.VRFConfiguration{}
	for _, vrf := range vrfConfigMap {
		vrfConfigs = append(vrfConfigs, vrf)
	}

	sort.SliceStable(vrfConfigs, func(i, j int) bool {
		return vrfConfigs[i].VNI < vrfConfigs[j].VNI
	})

	created, err := r.reconcileL3Netlink(vrfConfigs)
	if err != nil {
		r.Logger.Error(err, "error reconciling Netlink")
		return err
	}

	// We wait here for two seconds to let FRR settle after updating netlink devices
	time.Sleep(2 * time.Second)

	changed, err := r.frrManager.Configure(frr.FRRConfiguration{
		VRFs: vrfConfigs,
		ASN:  r.config.ServerASN,
	})
	if err != nil {
		r.Logger.Error(err, "error updating FRR configuration")
		return err
	}

	if changed || r.dirtyFRRConfig {
		r.Logger.Info("trying to reload FRR config because it changed")
		err = r.frrManager.ReloadFRR()
		if err != nil {
			r.dirtyFRRConfig = true
			r.Logger.Error(err, "error reloading FRR systemd unit")
			return err
		}
		r.dirtyFRRConfig = false
	}

	// Make sure that all created netlink VRFs are up after FRR reload
	time.Sleep(2 * time.Second)
	for _, info := range created {
		if err := r.netlinkManager.UpL3(info); err != nil {
			r.Logger.Error(err, "error setting L3 to state UP")
			return err
		}
	}
	return nil
}

func (r *reconcile) reconcileL3Netlink(vrfConfigs []frr.VRFConfiguration) ([]nl.VRFInformation, error) {
	existing, err := r.netlinkManager.ListL3()
	if err != nil {
		return nil, err
	}

	// Check for VRFs that are configured on the host but no longer in Kubernetes
	delete := []nl.VRFInformation{}
	for _, cfg := range existing {
		stillExists := false
		for _, vrf := range vrfConfigs {
			if vrf.Name == cfg.Name && vrf.VNI == cfg.VNI {
				stillExists = true
				break
			}
		}
		if !stillExists {
			delete = append(delete, cfg)
		}
	}

	// Check for VRFs that are in Kubernetes but not yet configured on the host
	create := []nl.VRFInformation{}
	for _, vrf := range vrfConfigs {
		// Skip VRF with VNI SKIP_VRF_TEMPLATE_VNI
		if vrf.VNI == config.SKIP_VRF_TEMPLATE_VNI {
			continue
		}
		alreadyExists := false
		for _, cfg := range existing {
			if vrf.Name == cfg.Name && vrf.VNI == cfg.VNI {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			create = append(create, nl.VRFInformation{
				Name: vrf.Name,
				VNI:  vrf.VNI,
			})
		}
	}

	// Delete / Cleanup VRFs
	for _, info := range delete {
		r.Logger.Info("Deleting VRF because it is no longer configured in Kubernetes", "vrf", info.Name, "vni", info.VNI)
		errs := r.netlinkManager.CleanupL3(info.Name)
		for _, err := range errs {
			r.Logger.Error(err, "Error deleting VRF", "vrf", info.Name, "vni", strconv.Itoa(info.VNI))
		}
	}
	// Create VRFs
	for _, info := range create {
		r.Logger.Info("Creating VRF to match Kubernetes", "vrf", info.Name, "vni", info.VNI)
		err := r.netlinkManager.CreateL3(info)
		if err != nil {
			return nil, fmt.Errorf("error creating VRF %s, VNI %d: %w", info.Name, info.VNI, err)
		}
	}

	return create, nil
}

func handlePrefixItemList(input []networkv1alpha1.VrfRouteConfigurationPrefixItem, seq int) (frr.PrefixList, error) {
	prefixList := frr.PrefixList{
		Seq: seq + 1,
	}
	for i, item := range input {
		frrItem, err := copyPrefixItemToFRRItem(i, item)
		if err != nil {
			return frr.PrefixList{}, err
		}
		prefixList.Items = append(prefixList.Items, frrItem)
	}
	return prefixList, nil
}

func copyPrefixItemToFRRItem(n int, item networkv1alpha1.VrfRouteConfigurationPrefixItem) (frr.PrefixedRouteItem, error) {
	_, network, err := net.ParseCIDR(item.CIDR)
	if err != nil {
		return frr.PrefixedRouteItem{}, err
	}

	seq := item.Seq
	if seq <= 0 {
		seq = n + 1
	}
	return frr.PrefixedRouteItem{
		CIDR:   *network,
		IPv6:   network.IP.To4() == nil,
		Seq:    seq,
		Action: item.Action,
		GE:     item.GE,
		LE:     item.LE,
	}, nil
}
