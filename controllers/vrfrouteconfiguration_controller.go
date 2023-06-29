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
	"sort"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

// VRFRouteConfigurationReconciler reconciles a VRFRouteConfiguration object
type VRFRouteConfigurationReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Config      *config.Config
	Debouncer   *debounce.Debouncer
	Logger      logr.Logger
	FRRManager  *frr.FRRManager
	NLManager   *nl.NetlinkManager
	dirtyConfig bool
}

//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=vrfrouteconfigurations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=vrfrouteconfigurations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.schiff.telekom.de,resources=vrfrouteconfigurations/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *VRFRouteConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Run ReconcileDebounced through debouncer
	r.Debouncer.Debounce(ctx)

	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VRFRouteConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Debouncer = debounce.NewDebouncer(r.ReconcileDebounced, 30*time.Second)
	r.Logger = mgr.GetLogger().WithName("vrf-controller")
	r.FRRManager = frr.NewFRRManager()
	r.NLManager = &nl.NetlinkManager{}

	if val := os.Getenv("FRR_CONFIG_FILE"); val != "" {
		r.FRRManager.ConfigPath = val
	}

	if err := r.FRRManager.Init(); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1alpha1.VRFRouteConfiguration{}).
		Complete(r)
}

func (r *VRFRouteConfigurationReconciler) ReconcileDebounced(ctx context.Context) error {
	vrfs := &networkv1alpha1.VRFRouteConfigurationList{}
	err := r.Client.List(ctx, vrfs)
	if err != nil {
		r.Logger.Error(err, "error getting list of VRFs from Kubernetes")
		return err
	}

	vrfConfigMap := map[string]frr.VRFConfiguration{}

	for _, vrf := range vrfs.Items {
		spec := vrf.Spec

		var vni int
		var rt string

		if val, ok := r.Config.VRFToVNI[spec.VRF]; ok {
			vni = val
			r.Logger.Info("Configuring VRF from old VRFToVNI", "vrf", spec.VRF, "vni", val)
		} else if val, ok := r.Config.VRFConfig[spec.VRF]; ok {
			vni = val.VNI
			rt = val.RT
			r.Logger.Info("Configuring VRF from new VRFConfig", "vrf", spec.VRF, "vni", val.VNI, "rt", rt)
		} else if r.Config.ShouldSkipVRFConfig(spec.VRF) {
			vni = config.SKIP_VRF_TEMPLATE_VNI
		} else {
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
			config.Export = append(config.Export, handlePrefixItemList(spec.Export, spec.Seq))
		}
		if len(spec.Import) > 0 {
			config.Import = append(config.Import, handlePrefixItemList(spec.Import, spec.Seq))
		}
		for _, aggregate := range spec.Aggregate {
			_, network, _ := net.ParseCIDR(aggregate)
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

	created, err := r.reconcileNetlink(vrfConfigs)
	if err != nil {
		r.Logger.Error(err, "error reconciling Netlink")
		return err
	}

	// We wait here for two seconds to let FRR settle after updating netlink devices
	time.Sleep(2 * time.Second)

	changed, err := r.FRRManager.Configure(frr.FRRConfiguration{
		VRFs: vrfConfigs,
		ASN:  r.Config.ServerASN,
	})
	if err != nil {
		r.Logger.Error(err, "error updating FRR configuration")
		return err
	}

	if changed || r.dirtyConfig {
		r.Logger.Info("trying to reload FRR config because it changed")
		err = r.FRRManager.ReloadFRR()
		if err != nil {
			r.dirtyConfig = true
			r.Logger.Error(err, "error reloading FRR systemd unit")
			return err
		}
		r.dirtyConfig = false
	}

	// Make sure that all created netlink VRFs are up after FRR reload
	time.Sleep(2 * time.Second)
	for _, info := range created {
		if err := r.NLManager.UpL3(info); err != nil {
			r.Logger.Error(err, "error setting L3 to state UP")
			return err
		}
	}
	return nil
}

func (r *VRFRouteConfigurationReconciler) reconcileNetlink(vrfConfigs []frr.VRFConfiguration) ([]nl.VRFInformation, error) {
	existing, err := r.NLManager.ListL3()
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
		errs := r.NLManager.CleanupL3(info.Name)
		for _, err := range errs {
			r.Logger.Error(err, "Error deleting VRF", "vrf", info.Name, "vni", strconv.Itoa(info.VNI))
		}
	}
	// Create VRFs
	for _, info := range create {
		r.Logger.Info("Creating VRF to match Kubernetes", "vrf", info.Name, "vni", info.VNI)
		err := r.NLManager.CreateL3(info)
		if err != nil {
			return nil, fmt.Errorf("error creating VRF %s, VNI %d: %w", info.Name, info.VNI, err)
		}
	}

	return create, nil
}

func handlePrefixItemList(input []networkv1alpha1.VrfRouteConfigurationPrefixItem, seq int) frr.PrefixList {
	prefixList := frr.PrefixList{
		Seq: seq + 1,
	}
	for i, item := range input {
		prefixList.Items = append(prefixList.Items, copyPrefixItemToFRRItem(i, item))
	}
	return prefixList
}

func copyPrefixItemToFRRItem(i int, item networkv1alpha1.VrfRouteConfigurationPrefixItem) frr.PrefixedRouteItem {
	_, network, _ := net.ParseCIDR(item.CIDR)

	seq := item.Seq
	if seq <= 0 {
		seq = i + 1
	}
	return frr.PrefixedRouteItem{
		CIDR:   *network,
		IPv6:   network.IP.To4() == nil,
		Seq:    seq,
		Action: item.Action,
		GE:     item.GE,
		LE:     item.LE,
	}
}
