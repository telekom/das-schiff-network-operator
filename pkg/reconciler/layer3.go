package reconciler

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"time"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"k8s.io/apimachinery/pkg/types"
)

const defaultSleep = 2 * time.Second

func (r *reconcile) fetchNodeConfig(ctx context.Context) (*networkv1alpha1.NodeConfig, error) {
	cfg := &networkv1alpha1.NodeConfig{}
	err := r.client.Get(ctx, types.NamespacedName{Name: os.Getenv(healthcheck.NodenameEnv)}, cfg)
	if err != nil {
		return nil, fmt.Errorf("error getting NodeConfig: %w", err)
	}
	return cfg, nil
}

// nolint: contextcheck // context is not relevant
func (r *reconcile) reconcileLayer3(l3vnis []networkv1alpha1.VRFRouteConfigurationSpec, taas []networkv1alpha1.RoutingTableSpec) error {
	vrfConfigMap, err := r.createVrfConfigMap(l3vnis)
	if err != nil {
		return err
	}

	vrfFromTaas := createVrfFromTaaS(taas)

	allConfigs := []frr.VRFConfiguration{}
	l3Configs := []frr.VRFConfiguration{}
	taasConfigs := []frr.VRFConfiguration{}
	for key := range vrfConfigMap {
		allConfigs = append(allConfigs, vrfConfigMap[key])
		l3Configs = append(l3Configs, vrfConfigMap[key])
	}
	for key := range vrfFromTaas {
		allConfigs = append(allConfigs, vrfFromTaas[key])
		taasConfigs = append(taasConfigs, vrfFromTaas[key])
	}

	sort.SliceStable(allConfigs, func(i, j int) bool {
		return allConfigs[i].VNI < allConfigs[j].VNI
	})

	created, deletedVRF, err := r.reconcileL3Netlink(l3Configs)
	if err != nil {
		r.Logger.Error(err, "error reconciling Netlink")
		return err
	}

	deletedTaas, err := r.reconcileTaasNetlink(taasConfigs)
	if err != nil {
		return err
	}
	reloadTwice := deletedVRF || deletedTaas

	// We wait here for two seconds to let FRR settle after updating netlink devices
	time.Sleep(defaultSleep)

	err = r.configureFRR(allConfigs, reloadTwice)
	if err != nil {
		return err
	}

	// Make sure that all created netlink VRFs are up after FRR reload
	time.Sleep(defaultSleep)
	for _, info := range created {
		if err := r.netlinkManager.UpL3(info); err != nil {
			r.Logger.Error(err, "error setting L3 to state UP")
			return fmt.Errorf("error setting L3 to state UP: %w", err)
		}
	}
	return nil
}

func (r *reconcile) configureFRR(vrfConfigs []frr.VRFConfiguration, reloadTwice bool) error {
	changed, err := r.frrManager.Configure(frr.Configuration{
		VRFs: vrfConfigs,
		ASN:  r.config.ServerASN,
	}, r.netlinkManager)
	if err != nil {
		r.Logger.Error(err, "error updating FRR configuration")
		return fmt.Errorf("error updating FRR configuration: %w", err)
	}

	if changed || r.dirtyFRRConfig {
		err := r.reloadFRR()
		if err != nil {
			r.dirtyFRRConfig = true
			return err
		}

		// When a BGP VRF is deleted there is a leftover running configuration after reload
		// A second reload fixes this.
		if reloadTwice {
			err := r.reloadFRR()
			if err != nil {
				r.dirtyFRRConfig = true
				return err
			}
		}
		r.dirtyFRRConfig = false
	}
	return nil
}

func (r *reconcile) reloadFRR() error {
	r.Logger.Info("trying to reload FRR config because it changed")
	err := r.frrManager.ReloadFRR()
	if err != nil {
		r.Logger.Error(err, "error reloading FRR systemd unit, trying restart")

		err = r.frrManager.RestartFRR()
		if err != nil {
			r.Logger.Error(err, "error restarting FRR systemd unit")
			return fmt.Errorf("error reloading / restarting FRR systemd unit: %w", err)
		}
	}
	r.Logger.Info("reloaded FRR config")
	return nil
}

func (r *reconcile) createVrfConfigMap(l3vnis []networkv1alpha1.VRFRouteConfigurationSpec) (map[string]frr.VRFConfiguration, error) {
	vrfConfigMap := map[string]frr.VRFConfiguration{}
	for i := range l3vnis {
		spec := l3vnis[i]
		logger := r.Logger.WithValues("vrf", spec.VRF)

		var vni int
		var rt string

		if val, ok := r.config.VRFConfig[spec.VRF]; ok {
			vni = val.VNI
			rt = val.RT
			logger.Info("Configuring VRF from new VRFConfig", "vni", val.VNI, "rt", rt)
		} else if val, ok := r.config.VRFToVNI[spec.VRF]; ok {
			vni = val
			logger.Info("Configuring VRF from old VRFToVNI", "vni", val)
		} else if r.config.ShouldSkipVRFConfig(spec.VRF) {
			vni = config.SkipVrfTemplateVni
		} else {
			err := fmt.Errorf("vrf not in vrf vni map")
			r.Logger.Error(err, "VRF does not exist in VRF VNI config, ignoring", "vrf", spec.VRF)
			continue
		}

		if vni == 0 && vni > 16777215 {
			err := fmt.Errorf("VNI can not be set to 0")
			r.Logger.Error(err, "VNI can not be set to 0, ignoring", "vrf", spec.VRF, "name")
			continue
		}

		cfg, err := createVrfConfig(vrfConfigMap, &spec, vni, rt)
		if err != nil {
			return nil, err
		}
		vrfConfigMap[spec.VRF] = *cfg
	}

	return vrfConfigMap, nil
}

func createVrfFromTaaS(taas []networkv1alpha1.RoutingTableSpec) map[string]frr.VRFConfiguration {
	vrfConfigMap := map[string]frr.VRFConfiguration{}

	for i := range taas {
		spec := taas[i]

		name := fmt.Sprintf("taas.%d", spec.TableID)

		vrfConfigMap[name] = frr.VRFConfiguration{
			Name:   name,
			VNI:    spec.TableID,
			IsTaaS: true,
		}
	}

	return vrfConfigMap
}

func createVrfConfig(vrfConfigMap map[string]frr.VRFConfiguration, spec *networkv1alpha1.VRFRouteConfigurationSpec, vni int, rt string) (*frr.VRFConfiguration, error) {
	// If VRF is not yet in dict, initialize it
	if _, ok := vrfConfigMap[spec.VRF]; !ok {
		vrfConfigMap[spec.VRF] = frr.VRFConfiguration{
			Name: spec.VRF,
			VNI:  vni,
			RT:   rt,
			MTU:  spec.MTU,
		}
	}

	cfg := vrfConfigMap[spec.VRF]

	if len(spec.Export) > 0 {
		prefixList, err := handlePrefixItemList(spec.Export, spec.Seq, spec.Community)
		if err != nil {
			return nil, err
		}
		cfg.Export = append(cfg.Export, prefixList)
	}
	if len(spec.Import) > 0 {
		prefixList, err := handlePrefixItemList(spec.Import, spec.Seq, nil)
		if err != nil {
			return nil, err
		}
		cfg.Import = append(cfg.Import, prefixList)
	}
	for _, aggregate := range spec.Aggregate {
		_, network, err := net.ParseCIDR(aggregate)
		if err != nil {
			return nil, fmt.Errorf("error parsing CIDR %s: %w", aggregate, err)
		}
		if network.IP.To4() == nil {
			cfg.AggregateIPv6 = append(cfg.AggregateIPv6, aggregate)
		} else {
			cfg.AggregateIPv4 = append(cfg.AggregateIPv4, aggregate)
		}
	}
	return &cfg, nil
}

func (r *reconcile) reconcileL3Netlink(vrfConfigs []frr.VRFConfiguration) ([]nl.VRFInformation, bool, error) {
	existing, err := r.netlinkManager.ListL3()
	if err != nil {
		return nil, false, fmt.Errorf("error listing L3 VRF information: %w", err)
	}

	// Check for VRFs that are configured on the host but no longer in Kubernetes
	toDelete := []nl.VRFInformation{}
	for i := range existing {
		stillExists := false
		for j := range vrfConfigs {
			if vrfConfigs[j].Name == existing[i].Name && vrfConfigs[j].VNI == existing[i].VNI {
				stillExists = true
				existing[i].MTU = vrfConfigs[j].MTU
				break
			}
		}
		if !stillExists || existing[i].MarkForDelete {
			toDelete = append(toDelete, existing[i])
		} else if err := r.reconcileExisting(existing[i]); err != nil {
			r.Logger.Error(err, "error reconciling existing VRF", "vrf", existing[i].Name, "vni", strconv.Itoa(existing[i].VNI))
		}
	}

	// Check for VRFs that are in Kubernetes but not yet configured on the host
	toCreate := prepareVRFsToCreate(vrfConfigs, existing)

	// Delete / Cleanup VRFs
	for _, info := range toDelete {
		r.Logger.Info("Deleting VRF because it is no longer configured in Kubernetes", "vrf", info.Name, "vni", info.VNI)
		errs := r.netlinkManager.CleanupL3(info.Name)
		for _, err := range errs {
			r.Logger.Error(err, "Error deleting VRF", "vrf", info.Name, "vni", strconv.Itoa(info.VNI))
		}
	}
	// Create VRFs
	for _, info := range toCreate {
		r.Logger.Info("Creating VRF to match Kubernetes", "vrf", info.Name, "vni", info.VNI)
		err := r.netlinkManager.CreateL3(info)
		if err != nil {
			return nil, false, fmt.Errorf("error creating VRF %s, VNI %d: %w", info.Name, info.VNI, err)
		}
	}

	return toCreate, len(toDelete) > 0, nil
}

func (r *reconcile) reconcileTaasNetlink(vrfConfigs []frr.VRFConfiguration) (bool, error) {
	existing, err := r.netlinkManager.ListTaas()
	if err != nil {
		return false, fmt.Errorf("error listing TaaS VRF information: %w", err)
	}

	deletedInterface, err := r.cleanupTaasNetlink(existing, vrfConfigs)
	if err != nil {
		return false, err
	}

	err = r.createTaasNetlink(existing, vrfConfigs)
	if err != nil {
		return false, err
	}

	return deletedInterface, nil
}

func (r *reconcile) cleanupTaasNetlink(existing []nl.TaasInformation, intended []frr.VRFConfiguration) (bool, error) {
	deletedInterface := false
	for _, cfg := range existing {
		stillExists := false
		for i := range intended {
			if intended[i].Name == cfg.Name && intended[i].VNI == cfg.Table {
				stillExists = true
			}
		}
		if !stillExists {
			deletedInterface = true
			err := r.netlinkManager.CleanupTaas(cfg)
			if err != nil {
				return false, fmt.Errorf("error deleting TaaS %s, table %d: %w", cfg.Name, cfg.Table, err)
			}
		}
	}
	return deletedInterface, nil
}

func (r *reconcile) createTaasNetlink(existing []nl.TaasInformation, intended []frr.VRFConfiguration) error {
	for i := range intended {
		alreadyExists := false
		for _, cfg := range existing {
			if intended[i].Name == cfg.Name && intended[i].VNI == cfg.Table {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			info := nl.TaasInformation{
				Name:  intended[i].Name,
				Table: intended[i].VNI,
			}
			err := r.netlinkManager.CreateTaas(info)
			if err != nil {
				return fmt.Errorf("error creating Taas %s, table %d: %w", info.Name, info.Table, err)
			}
		}
	}
	return nil
}

func (r *reconcile) reconcileExisting(cfg nl.VRFInformation) error {
	if err := r.netlinkManager.EnsureBPFProgram(cfg); err != nil {
		return fmt.Errorf("error ensuring BPF program on VRF")
	}
	if err := r.netlinkManager.EnsureMTU(cfg); err != nil {
		return fmt.Errorf("error setting VRF veth link MTU: %d", cfg.MTU)
	}
	return nil
}

func prepareVRFsToCreate(vrfConfigs []frr.VRFConfiguration, existing []nl.VRFInformation) []nl.VRFInformation {
	create := []nl.VRFInformation{}
	for i := range vrfConfigs {
		// Skip VRF with VNI SKIP_VRF_TEMPLATE_VNI
		if vrfConfigs[i].VNI == config.SkipVrfTemplateVni {
			continue
		}
		alreadyExists := false
		for _, cfg := range existing {
			if vrfConfigs[i].Name == cfg.Name && vrfConfigs[i].VNI == cfg.VNI && !cfg.MarkForDelete {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			create = append(create, nl.VRFInformation{
				Name: vrfConfigs[i].Name,
				VNI:  vrfConfigs[i].VNI,
				MTU:  vrfConfigs[i].MTU,
			})
		}
	}
	return create
}

func handlePrefixItemList(input []networkv1alpha1.VrfRouteConfigurationPrefixItem, seq int, community *string) (frr.PrefixList, error) {
	prefixList := frr.PrefixList{
		Seq:       seq + 1,
		Community: community,
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
		return frr.PrefixedRouteItem{}, fmt.Errorf("error parsing CIDR :%s: %w", item.CIDR, err)
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

func (r *reconcileConfig) fetchLayer3(ctx context.Context) ([]networkv1alpha1.VRFRouteConfiguration, error) {
	vrfs := &networkv1alpha1.VRFRouteConfigurationList{}
	err := r.client.List(ctx, vrfs)
	if err != nil {
		r.Logger.Error(err, "error getting list of VRFs from Kubernetes")
		return nil, fmt.Errorf("error getting list of VRFs from Kubernetes: %w", err)
	}

	return vrfs.Items, nil
}

func (r *reconcileConfig) fetchTaas(ctx context.Context) ([]networkv1alpha1.RoutingTable, error) {
	tables := &networkv1alpha1.RoutingTableList{}
	err := r.client.List(ctx, tables)
	if err != nil {
		r.Logger.Error(err, "error getting list of TaaS from Kubernetes")
		return nil, fmt.Errorf("error getting list of TaaS from Kubernetes: %w", err)
	}

	return tables.Items, nil
}
