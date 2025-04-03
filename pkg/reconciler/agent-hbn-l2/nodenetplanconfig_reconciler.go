package agent_hbn_l2 //nolint:revive

import (
	"context"
	"fmt"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
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
	vlanNamePrefix  = "vlan"
	dummyNamePrefix = "dummy"
	aliasPrefix     = "hbn"
)

type addresses struct {
	Addresses []string `json:"addresses"`
}

type netplanVlan struct {
	addresses `json:",inline"`
	Id        int `json:"id"`
	Mtu       int `json:"mtu"`
}

type netplanDummy struct {
	addresses `json:",inline"`
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
nmstate or netplan. This just creates VLAN and dummy interfaces and nothing else (for now...).
*/

func (reconciler *NodeNetplanConfigReconciler) reconcileVLANs(devices map[string]netplan.Device) error {
	masterInterface, err := netlink.LinkByName(hbnMasterName)
	if err != nil {
		return fmt.Errorf("error getting master interface: %w", err)
	}

	vlanAliasPrefix := fmt.Sprintf("%s::%s::", aliasPrefix, vlanNamePrefix)

	allInterfaces, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("error listing interfaces: %w", err)
	}

	for _, existingInterface := range allInterfaces {
		if existingInterface.Type() == "vlan" {
			alias := existingInterface.Attrs().Alias
			if strings.HasPrefix(alias, vlanAliasPrefix) {
				name := strings.TrimPrefix(alias, vlanAliasPrefix)
				if _, ok := devices[name]; !ok {
					// remove vlan
					if err := netlink.LinkDel(existingInterface); err != nil {
						return fmt.Errorf("error deleting vlan %s: %w", existingInterface.Attrs().Name, err)
					}
				} else {
					if err := setEUIAutogeneration(existingInterface.Attrs().Name, false); err != nil {
						return fmt.Errorf("error setting EUI autogeneration: %w", err)
					}
					if err := reconciler.reconcileAddresses(existingInterface, devices[name]); err != nil {
						return fmt.Errorf("error reconciling addresses for vlan %s: %w", existingInterface.Attrs().Name, err)
					}
				}
			}
		}
	}

	for name, vlan := range devices {
		vlanConfig, err := parseVlan(vlan)
		if err != nil {
			return fmt.Errorf("error parsing vlan config: %w", err)
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
					Name:        fmt.Sprintf("%s.%d", vlanNamePrefix, vlanConfig.Id),
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
			if err := reconciler.reconcileAddresses(&link, vlan); err != nil {
				return fmt.Errorf("error reconciling addresses for vlan %s: %w", name, err)
			}
		}
	}

	return nil
}

func (reconciler *NodeNetplanConfigReconciler) removeNotExistingAddresses(link netlink.Link, addresses []string, family int) error {
	allAddresses, err := netlink.AddrList(link, family)
	if err != nil {
		return fmt.Errorf("error listing link's addresses: %w", err)
	}
	for _, addr := range allAddresses {
		existing := false
		for _, address := range addresses {
			if addr.String() == address {
				existing = true
				break
			}
		}
		if !existing {
			if err := netlink.AddrDel(link, &addr); err != nil {
				return fmt.Errorf("error deleting address %s from link %s: %w", addr.String(), link.Attrs().Name, err)
			}
		}
	}

	return nil
}

func (reconciler *NodeNetplanConfigReconciler) reconcileAddresses(link netlink.Link, device netplan.Device) error {
	addressConfig, err := parseAddresses(device)
	if err != nil {
		return fmt.Errorf("error parsing addresses config: %w", err)
	}
	for _, address := range addressConfig.Addresses {
		addr, err := netlink.ParseAddr(address)
		if err != nil {
			return fmt.Errorf("error parsing address %s: %w", address, err)
		}
		if err := netlink.AddrReplace(link, addr); err != nil {
			return fmt.Errorf("error adding address %s to link %s: %w", address, link.Attrs().Name, err)
		}
	}

	if err := reconciler.removeNotExistingAddresses(link, addressConfig.Addresses, unix.AF_INET); err != nil {
		return fmt.Errorf("error removing not existing IPv4 addresses: %w", err)
	}
	if err := reconciler.removeNotExistingAddresses(link, addressConfig.Addresses, unix.AF_INET6); err != nil {
		return fmt.Errorf("error removing not existing IPv6 addresses: %w", err)
	}

	return nil
}

func (reconciler *NodeNetplanConfigReconciler) reconcileLoopbacks(devices map[string]netplan.Device) error {
	dummyAliasPrefix := fmt.Sprintf("%s::%s::", aliasPrefix, dummyNamePrefix)

	allInterfaces, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("error listing interfaces: %w", err)
	}

	for _, existingInterface := range allInterfaces {
		if existingInterface.Type() == "dummy" {
			alias := existingInterface.Attrs().Alias
			if strings.HasPrefix(alias, dummyAliasPrefix) {
				name := strings.TrimPrefix(alias, dummyAliasPrefix)
				if _, ok := devices[name]; !ok {
					// remove dummy
					if err := netlink.LinkDel(existingInterface); err != nil {
						return fmt.Errorf("error deleting dummy %s: %w", existingInterface.Attrs().Name, err)
					}
				} else {
					if err := setEUIAutogeneration(existingInterface.Attrs().Name, false); err != nil {
						return fmt.Errorf("error setting EUI autogeneration: %w", err)
					}
					if err := reconciler.reconcileAddresses(existingInterface, devices[name]); err != nil {
						return fmt.Errorf("error reconciling addresses for dummy %s: %w", existingInterface.Attrs().Name, err)
					}
				}
			}
		}
	}

	for name, dummy := range devices {
		existing := false
		for _, existingInterface := range allInterfaces {
			if existingInterface.Type() == "dummy" {
				alias := existingInterface.Attrs().Alias
				if strings.HasPrefix(alias, dummyAliasPrefix) && strings.TrimPrefix(alias, dummyAliasPrefix) == name {
					existing = true
					break
				}
			}
		}
		if !existing {
			link := netlink.Dummy{
				LinkAttrs: netlink.LinkAttrs{
					Name: name,
				},
			}
			if err := netlink.LinkAdd(&link); err != nil {
				return fmt.Errorf("error adding dummy %s: %w", name, err)
			}
			if err := netlink.LinkSetAlias(&link, fmt.Sprintf("%s%s", dummyAliasPrefix, name)); err != nil {
				return fmt.Errorf("error setting alias for dummy %s: %w", name, err)
			}
			if err := setEUIAutogeneration(link.Attrs().Name, false); err != nil {
				return fmt.Errorf("error setting EUI autogeneration: %w", err)
			}
			if err := netlink.LinkSetUp(&link); err != nil {
				return fmt.Errorf("error setting up vlan %s: %w", name, err)
			}
			if err := reconciler.reconcileAddresses(&link, dummy); err != nil {
				return fmt.Errorf("error reconciling addresses for dummy %s: %w", name, err)
			}
		}
	}

	return nil
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

	if err := r.reconcileVLANs(cfg.Spec.DesiredState.Network.VLans); err != nil {
		return fmt.Errorf("error reconciling VLANs: %w", err)
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

func parseVlan(device netplan.Device) (*netplanVlan, error) {
	vlan := &netplanVlan{}
	if err := yaml.Unmarshal(device.Raw, vlan); err != nil {
		return nil, fmt.Errorf("error unmarshalling vlan config: %w", err)
	}
	return vlan, nil
}

func parseDummy(device netplan.Device) (*netplanDummy, error) {
	dummy := &netplanDummy{}
	if err := yaml.Unmarshal(device.Raw, dummy); err != nil {
		return nil, fmt.Errorf("error unmarshalling dummy config: %w", err)
	}
	return dummy, nil
}

func parseAddresses(device netplan.Device) (*addresses, error) {
	addr := &addresses{}
	if err := yaml.Unmarshal(device.Raw, addr); err != nil {
		return nil, fmt.Errorf("error unmarshalling address config: %w", err)
	}
	return addr, nil
}
