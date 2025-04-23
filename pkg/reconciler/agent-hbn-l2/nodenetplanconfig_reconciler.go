package agent_hbn_l2 //nolint:revive

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
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

var (
	dummyAliasPrefix = fmt.Sprintf("%s::%s::", aliasPrefix, dummyNamePrefix)
	vlanAliasPrefix  = fmt.Sprintf("%s::%s::", aliasPrefix, vlanNamePrefix)
)

type addresses struct {
	Addresses []string `json:"addresses"`
}

type netplanVlan struct {
	addresses `json:",inline"`
	ID        int `json:"id"`
	Mtu       int `json:"mtu"`
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

func createVLAN(masterInterface netlink.Link, vlanConfig *netplanVlan, name string, vlan netplan.Device, bridge *netlink.Bridge) error {
	link := netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        fmt.Sprintf("%s.%d", vlanNamePrefix, vlanConfig.ID),
			ParentIndex: masterInterface.Attrs().Index,
			MTU:         vlanConfig.Mtu,
		},
		VlanId: vlanConfig.ID,
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
	if err := reconcileAddresses(&link, vlan); err != nil {
		return fmt.Errorf("error reconciling addresses for vlan %s: %w", name, err)
	}
	if bridge != nil {
		vlanId, err := parseVlanId(vlanConfig.ID)
		if err != nil {
			return fmt.Errorf("error parsing vlan ID %d: %w", vlanConfig.ID, err)
		}
		if err := netlink.BridgeVlanAdd(bridge, vlanId, false, false, true, false); err != nil {
			return fmt.Errorf("error adding vlan %d to bridge %s: %w", vlanConfig.ID, (*bridge).Attrs().Name, err)
		}
	}
	return nil
}

func reconcileVLANs(devices map[string]netplan.Device) error {
	masterInterface, err := netlink.LinkByName(hbnMasterName)
	if err != nil {
		return fmt.Errorf("error getting master interface: %w", err)
	}
	var bridge *netlink.Bridge
	if masterInterface.Type() == "bridge" {
		if intf, ok := masterInterface.(*netlink.Bridge); ok {
			bridge = intf
		}
	}

	allInterfaces, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("error listing interfaces: %w", err)
	}

	if err := reconcileExisting(vlanAliasPrefix, "vlan", devices, allInterfaces, bridge); err != nil {
		return fmt.Errorf("error reconciling existing vlans: %w", err)
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
			if err := createVLAN(masterInterface, vlanConfig, name, vlan, bridge); err != nil {
				return fmt.Errorf("error creating vlan %s: %w", name, err)
			}
		}
	}

	return nil
}

func removeNotExistingAddresses(link netlink.Link, addresses []string, family int) error {
	allAddresses, err := netlink.AddrList(link, family)
	if err != nil {
		return fmt.Errorf("error listing link's addresses: %w", err)
	}
	for i := range allAddresses {
		existing := false
		for _, address := range addresses {
			if allAddresses[i].IPNet.String() == address {
				existing = true
				break
			}
		}
		if !existing {
			if err := netlink.AddrDel(link, &allAddresses[i]); err != nil {
				return fmt.Errorf("error deleting address %s from link %s: %w", allAddresses[i].String(), link.Attrs().Name, err)
			}
		}
	}

	return nil
}

func reconcileAddresses(link netlink.Link, device netplan.Device) error {
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

	if err := removeNotExistingAddresses(link, addressConfig.Addresses, unix.AF_INET); err != nil {
		return fmt.Errorf("error removing not existing IPv4 addresses: %w", err)
	}
	if err := removeNotExistingAddresses(link, addressConfig.Addresses, unix.AF_INET6); err != nil {
		return fmt.Errorf("error removing not existing IPv6 addresses: %w", err)
	}

	return nil
}

func parseVlanId(vlan int) (uint16, error) {
	if vlan < 0 || vlan > 4095 {
		return 0, fmt.Errorf("vlan ID %d is out of bounds (0-4095)", vlan)
	}
	return uint16(vlan), nil
}

func deleteBridgeVlan(bridge *netlink.Bridge, existing netlink.Link) error {
	if existing.Type() != "vlan" {
		return fmt.Errorf("error deleting vlan %s: not a vlan", existing.Attrs().Name)
	}

	var vlanID uint16
	if vlan, ok := existing.(*netlink.Vlan); ok {
		if parsedVlan, err := parseVlanId(vlan.VlanId); err != nil {
			return fmt.Errorf("error parsing vlan ID %d: %w", vlan.VlanId, err)
		} else {
			vlanID = parsedVlan
		}
	} else {
		return fmt.Errorf("error deleting vlan %s: not a vlan", existing.Attrs().Name)
	}

	if err := netlink.BridgeVlanDel(bridge, vlanID, false, false, true, false); err != nil {
		return fmt.Errorf("error deleting vlan %d from bridge %s: %w", vlanID, (*bridge).Attrs().Name, err)
	}
	return nil
}

func reconcileExistingAddresses(existingInterface netlink.Link, device netplan.Device) error {
	if err := setEUIAutogeneration(existingInterface.Attrs().Name, false); err != nil {
		return fmt.Errorf("error setting EUI autogeneration: %w", err)
	}
	if err := reconcileAddresses(existingInterface, device); err != nil {
		return fmt.Errorf("error reconciling addresses for %s: %w", existingInterface.Attrs().Name, err)
	}
	return nil
}

func reconcileExisting(prefix, interfaceType string, devices map[string]netplan.Device, allInterfaces []netlink.Link, bridge *netlink.Bridge) error {
	for _, existingInterface := range allInterfaces {
		if existingInterface.Type() == interfaceType {
			alias := existingInterface.Attrs().Alias
			if strings.HasPrefix(alias, prefix) {
				name := strings.TrimPrefix(alias, prefix)
				if _, ok := devices[name]; !ok {
					if err := netlink.LinkDel(existingInterface); err != nil {
						return fmt.Errorf("error deleting %s: %w", existingInterface.Attrs().Name, err)
					}
					if bridge != nil && existingInterface.Type() == "vlan" {
						if err := deleteBridgeVlan(bridge, existingInterface); err != nil {
							return fmt.Errorf("error deleting vlan %s from bridge %s: %w", existingInterface.Attrs().Name, (*bridge).Attrs().Name, err)
						}
					}
				} else {
					if err := reconcileExistingAddresses(existingInterface, devices[name]); err != nil {
						return fmt.Errorf("error reconciling existing addresses for %s: %w", existingInterface.Attrs().Name, err)
					}
				}
			}
		}
	}

	return nil
}

func reconcileLoopbacks(devices map[string]netplan.Device) error {
	allInterfaces, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("error listing interfaces: %w", err)
	}

	if err := reconcileExisting(dummyAliasPrefix, "dummy", devices, allInterfaces, nil); err != nil {
		return fmt.Errorf("error reconciling existing loopbacks: %w", err)
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
			if err := reconcileAddresses(&link, dummy); err != nil {
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

	if err := reconcileVLANs(cfg.Spec.DesiredState.Network.VLans); err != nil {
		return fmt.Errorf("error reconciling VLANs: %w", err)
	}
	if err := reconcileLoopbacks(cfg.Spec.DesiredState.Network.Dummies); err != nil {
		return fmt.Errorf("error reconciling loopbacks: %w", err)
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

func parseAddresses(device netplan.Device) (*addresses, error) {
	addr := &addresses{}
	if err := yaml.Unmarshal(device.Raw, addr); err != nil {
		return nil, fmt.Errorf("error unmarshalling address config: %w", err)
	}
	return addr, nil
}
