package agent_hbn_l2 //nolint:revive

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
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

/*
Yes, we duplicate code in here. This is a temporary solution until we have a better way to handle this, either by using
nmstate or netplan. This just creates the VLAN interfaces and nothing else.
*/

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
					if err := netlink.LinkDel(existingInterface); err != nil {
						return fmt.Errorf("error deleting vlan %s: %w", existingInterface.Attrs().Name, err)
					}
				} else {
					if err := reconcileEUIAutogeneration(existingInterface, false); err != nil {
						return fmt.Errorf("error setting EUI autogeneration: %w", err)
					}
				}
			}
		}
	}

	for name, vlan := range desiredVlans {
		vlanConfig := netplanVlan{}
		if err := yaml.Unmarshal(vlan.Raw, &vlanConfig); err != nil {
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
			if err := setEUIAutogeneration(link.Attrs().Name, false); err != nil {
				return fmt.Errorf("error setting EUI autogeneration: %w", err)
			}
			if err := netlink.LinkSetUp(&link); err != nil {
				return fmt.Errorf("error setting up vlan %s: %w", name, err)
			}
		}
	}

	return nil
}

func setEUIAutogeneration(intfName string, generateEUI bool) error {
	fileName := fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/addr_gen_mode", intfName)
	file, err := os.OpenFile(fileName, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()
	value := "1"
	if generateEUI {
		value = "0"
	}
	if _, err := fmt.Fprintf(file, "%s\n", value); err != nil {
		return fmt.Errorf("error writing to file: %w", err)
	}
	return nil
}

func reconcileEUIAutogeneration(intf netlink.Link, enableEUI bool) error {
	if err := setEUIAutogeneration(intf.Attrs().Name, enableEUI); err != nil {
		return fmt.Errorf("error setting EUI autogeneration: %w", err)
	}
	if !enableEUI {
		addresses, err := netlink.AddrList(intf, unix.AF_INET6)
		if err != nil {
			return fmt.Errorf("error listing link's IPv6 addresses: %w", err)
		}
		for i := range addresses {
			if addresses[i].IP.IsLinkLocalUnicast() {
				if err := netlink.AddrDel(intf, &addresses[i]); err != nil {
					return fmt.Errorf("error removing link local IPv6 address: %w", err)
				}
			}
		}
	}
	return nil
}
