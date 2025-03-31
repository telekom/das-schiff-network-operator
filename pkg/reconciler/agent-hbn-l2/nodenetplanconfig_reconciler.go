package agent_hbn_l2 //nolint:revive

import (
	"context"
	"fmt"
	"github.com/vishvananda/netlink"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"gopkg.in/yaml.v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	hbnMasterName   = "hbn"
	vlanNamePrefix  = "vlan."
	vlanAliasPrefix = "hbn::vlan::"
)

type netplanVlan struct {
	Id  int `json:"id"`
	Mtu int `json:"mtu"`
}

type NodeNetplanConfigReconciler struct {
	client client.Client
	logger logr.Logger
}

type reconcileNodeNetworkConfig struct {
	*NodeNetplanConfigReconciler
	logr.Logger
}

func NewNodeNetplanConfigReconciler(clusterClient client.Client, logger logr.Logger) (*NodeNetplanConfigReconciler, error) {
	reconciler := &NodeNetplanConfigReconciler{
		client: clusterClient,
		logger: logger,
	}

	return reconciler, nil
}

func (r *reconcileNodeNetworkConfig) fetchNodeConfig(ctx context.Context) (*v1alpha1.NodeNetplanConfig, error) {
	cfg := &v1alpha1.NodeNetplanConfig{}
	err := r.client.Get(ctx, types.NamespacedName{Name: os.Getenv(healthcheck.NodenameEnv)}, cfg)
	if err != nil {
		return nil, fmt.Errorf("error getting NodeConfig: %w", err)
	}
	return cfg, nil
}

func (reconciler *NodeNetplanConfigReconciler) Reconcile(ctx context.Context) error {
	r := &reconcileNodeNetworkConfig{
		NodeNetplanConfigReconciler: reconciler,
		Logger:                      reconciler.logger,
	}

	cfg, err := r.fetchNodeConfig(ctx)
	if err != nil {
		// discard IsNotFound error
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	masterInterface, err := netlink.LinkByName(hbnMasterName)
	if err != nil {
		return fmt.Errorf("error getting master interface: %w", err)
	}

	desiredVlans := cfg.Spec.DesiredState.Network.VLans

	allInterfaces, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("error listing interfaces: %w", err)
	}

	for _, existingInterface := range allInterfaces {
		if existingInterface.Type() == "vlan" {
			alias := existingInterface.Attrs().Alias
			if strings.HasPrefix(alias, vlanAliasPrefix) {
				name := strings.TrimPrefix(alias, vlanAliasPrefix)
				if _, ok := desiredVlans[name]; !ok {
					// remove vlan
					err := netlink.LinkDel(existingInterface)
					if err != nil {
						return fmt.Errorf("error deleting vlan %s: %w", existingInterface.Attrs().Name, err)
					}
				}
			}
		}
	}

	for name, vlan := range desiredVlans {
		vlanConfig := netplanVlan{}
		err := yaml.Unmarshal(vlan.Raw, &vlanConfig)
		if err != nil {
			return fmt.Errorf("error unmarshalling vlan config: %w", err)
		}

		existing := false
		for _, existingInterface := range allInterfaces {
			if existingInterface.Type() == "vlan" {
				alias := existingInterface.Attrs().Alias
				if strings.HasPrefix(alias, vlanAliasPrefix) && strings.TrimPrefix(alias, vlanAliasPrefix) == name {
					existing = true
					break
				}
			}
		}
		if !existing {
			link := netlink.Vlan{
				LinkAttrs: netlink.LinkAttrs{
					Name:        fmt.Sprintf("%s%d", vlanNamePrefix, vlanConfig.Id),
					ParentIndex: masterInterface.Attrs().Index,
					MTU:         vlanConfig.Mtu,
				},
				VlanId: vlanConfig.Id,
			}
			if err := netlink.LinkAdd(&link); err != nil {
				return fmt.Errorf("error adding vlan %s: %w", name, err)
			}
			if err := netlink.LinkSetAlias(&link, fmt.Sprintf("%s%s", vlanAliasPrefix, name)); err != nil {
				return fmt.Errorf("error setting alias for vlan %s: %w", name, err)
			}
			if err := netlink.LinkSetUp(&link); err != nil {
				return fmt.Errorf("error setting up vlan %s: %w", name, err)
			}
		}
	}

	return nil
}
