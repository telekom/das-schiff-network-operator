package agent_hbn_l2 //nolint:revive

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

const (
	hbnMasterName   = "hbn"
	vlanNamePrefix  = "vlan"
	dummyNamePrefix = "dummy"
	aliasPrefix     = "hbn"

	vlanInterfaceType   = "vlan"
	bridgeInterfaceType = "bridge"
	dummyInterfaceType  = "dummy"

	// rtprotHBN is the custom route protocol used to tag routes managed by the
	// HBN-L2 agent. Only routes with this protocol are cleaned up during
	// reconciliation, leaving kernel and other routes untouched.
	rtprotHBN netlink.RouteProtocol = 196
)

var (
	dummyAliasPrefix       = fmt.Sprintf("%s::%s::", aliasPrefix, dummyNamePrefix)
	vlanAliasPrefix        = fmt.Sprintf("%s::%s::", aliasPrefix, vlanNamePrefix)
	procSysNetIPv6ConfPath = "/proc/sys/net/ipv6/conf"
)

type addresses struct {
	Addresses []string `json:"addresses"`
}

type netplanVlan struct {
	addresses                  `json:",inline" yaml:",inline"`
	ID                         int           `json:"id" yaml:"id"`
	Mtu                        int           `json:"mtu" yaml:"mtu"`
	Routes                     []routeConfig `json:"routes,omitempty" yaml:"routes,omitempty"`
	GenericReceiveOffload      *bool         `json:"generic-receive-offload" yaml:"generic-receive-offload"`
	GenericSegmentationOffload *bool         `json:"generic-segmentation-offload" yaml:"generic-segmentation-offload"`
	TCPSegmentationOffload     *bool         `json:"tcp-segmentation-offload" yaml:"tcp-segmentation-offload"`
}

type routeConfig struct {
	To  string `json:"to" yaml:"to"`
	Via string `json:"via" yaml:"via"`
}

func (v *netplanVlan) disableSegmentation() bool {
	return isFalse(v.GenericReceiveOffload) || isFalse(v.GenericSegmentationOffload) || isFalse(v.TCPSegmentationOffload)
}

func isFalse(value *bool) bool {
	return value != nil && !*value
}

type NodeNetplanConfigReconciler struct {
	client        client.Client
	logger        logr.Logger
	healthChecker *healthcheck.HealthChecker
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

	// Load healthcheck config and create health checker.
	nc, err := healthcheck.LoadConfig(healthcheck.NetHealthcheckFile)
	if err != nil {
		return nil, fmt.Errorf("error loading networking healthcheck config: %w", err)
	}
	tcpDialer := healthcheck.NewTCPDialer(nc.Timeout)
	reconciler.healthChecker, err = healthcheck.NewHealthChecker(reconciler.client, healthcheck.NewDefaultHealthcheckToolkit(tcpDialer), nc)
	if err != nil {
		return nil, fmt.Errorf("error creating networking healthchecker: %w", err)
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
	if vlanConfig.disableSegmentation() {
		if err := nl.ReconcileSegmentation(&link, true); err != nil {
			return fmt.Errorf("error disabling segmentation offload for vlan %s: %w", name, err)
		}
	}
	if err := reconcileAddresses(&link, vlan); err != nil {
		return fmt.Errorf("error reconciling addresses for vlan %s: %w", name, err)
	}
	if err := reconcileRoutes(&link, vlan); err != nil {
		return fmt.Errorf("error reconciling routes for vlan %s: %w", name, err)
	}
	if bridge != nil {
		vlanID, err := parseVlanID(vlanConfig.ID)
		if err != nil {
			return fmt.Errorf("error parsing vlan ID %d: %w", vlanConfig.ID, err)
		}
		if err := netlink.BridgeVlanAdd(bridge, vlanID, false, false, true, false); err != nil {
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
	if masterInterface.Type() == bridgeInterfaceType {
		if intf, ok := masterInterface.(*netlink.Bridge); ok {
			bridge = intf
		}
	}

	allInterfaces, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("error listing interfaces: %w", err)
	}

	if err := reconcileExisting(vlanAliasPrefix, vlanInterfaceType, devices, allInterfaces, bridge); err != nil {
		return fmt.Errorf("error reconciling existing vlans: %w", err)
	}

	for name, vlan := range devices {
		vlanConfig, err := parseVlan(vlan)
		if err != nil {
			return fmt.Errorf("error parsing vlan config: %w", err)
		}

		existing := false
		for _, existingInterface := range allInterfaces {
			if existingInterface.Type() == vlanInterfaceType {
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

func parseVlanID(vlan int) (uint16, error) {
	if vlan < 0 || vlan > 4095 {
		return 0, fmt.Errorf("vlan ID %d is out of bounds (0-4095)", vlan)
	}
	return uint16(vlan), nil
}

func deleteBridgeVlan(bridge *netlink.Bridge, existing netlink.Link) error {
	if existing.Type() != vlanInterfaceType {
		return fmt.Errorf("error deleting vlan %s: not a vlan", existing.Attrs().Name)
	}

	vlan, ok := existing.(*netlink.Vlan)
	if !ok {
		return fmt.Errorf("error deleting vlan %s: not a vlan", existing.Attrs().Name)
	}

	parsedVlan, err := parseVlanID(vlan.VlanId)
	if err != nil {
		return fmt.Errorf("error parsing vlan ID %d: %w", vlan.VlanId, err)
	}

	if err := netlink.BridgeVlanDel(bridge, parsedVlan, false, false, true, false); err != nil {
		return fmt.Errorf("error deleting vlan %d from bridge %s: %w", parsedVlan, (*bridge).Attrs().Name, err)
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
	if err := reconcileRoutes(existingInterface, device); err != nil {
		return fmt.Errorf("error reconciling routes for %s: %w", existingInterface.Attrs().Name, err)
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
					if bridge != nil && existingInterface.Type() == vlanInterfaceType {
						if err := deleteBridgeVlan(bridge, existingInterface); err != nil {
							return fmt.Errorf("error deleting vlan %s from bridge %s: %w", existingInterface.Attrs().Name, (*bridge).Attrs().Name, err)
						}
					}
				} else if err := reconcileExistingInterface(existingInterface, interfaceType, devices[name]); err != nil {
					return fmt.Errorf("error reconciling existing %s: %w", existingInterface.Attrs().Name, err)
				}
			}
		}
	}

	return nil
}

func reconcileExistingInterface(existingInterface netlink.Link, interfaceType string, device netplan.Device) error {
	if err := reconcileExistingAddresses(existingInterface, device); err != nil {
		return err
	}
	if interfaceType != vlanInterfaceType {
		return nil
	}
	vlanConfig, err := parseVlan(device)
	if err != nil {
		return err
	}
	if !vlanConfig.disableSegmentation() {
		return nil
	}
	vlanInterface, ok := existingInterface.(*netlink.Vlan)
	if !ok {
		return fmt.Errorf("expected vlan link, got %s", existingInterface.Type())
	}
	if err := nl.ReconcileSegmentation(vlanInterface, true); err != nil {
		return fmt.Errorf("error disabling segmentation offload: %w", err)
	}
	return nil
}

func reconcileLoopbacks(devices map[string]netplan.Device) error {
	allInterfaces, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("error listing interfaces: %w", err)
	}

	if err := reconcileExisting(dummyAliasPrefix, dummyInterfaceType, devices, allInterfaces, nil); err != nil {
		return fmt.Errorf("error reconciling existing loopbacks: %w", err)
	}

	for name, dummy := range devices {
		existing := false
		for _, existingInterface := range allInterfaces {
			if existingInterface.Type() == dummyInterfaceType {
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
		_ = reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonVLANReconcileFailed, err.Error())
		return fmt.Errorf("error reconciling VLANs: %w", err)
	}
	if err := reconcileLoopbacks(cfg.Spec.DesiredState.Network.Dummies); err != nil {
		_ = reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonLoopbackReconcileFail, err.Error())
		return fmt.Errorf("error reconciling loopbacks: %w", err)
	}

	// Perform health checks (interfaces / reachability / API server)
	if err := reconciler.healthChecker.CheckInterfaces(); err != nil {
		_ = reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonInterfaceCheckFailed, err.Error())
		return fmt.Errorf("error checking network interfaces: %w", err)
	}
	if err := reconciler.healthChecker.CheckReachability(); err != nil {
		_ = reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonReachabilityFailed, err.Error())
		return fmt.Errorf("error checking network reachability: %w", err)
	}
	if err := reconciler.healthChecker.CheckAPIServer(ctx); err != nil {
		_ = reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonAPIServerFailed, err.Error())
		return fmt.Errorf("error checking API Server reachability: %w", err)
	}
	if err := reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionTrue, healthcheck.ReasonHealthChecksPassed, "All network operator health checks passed"); err != nil {
		reconciler.logger.Error(err, "failed to update network operator readiness condition")
	}
	if !reconciler.healthChecker.TaintsRemoved() {
		if err := reconciler.healthChecker.RemoveTaints(ctx); err != nil {
			return fmt.Errorf("error removing taint from the node: %w", err)
		}
	}

	return nil
}

func setEUIAutogeneration(intfName string, generateEUI bool) error {
	if err := nl.ValidateInterfaceName(intfName); err != nil {
		return fmt.Errorf("validate interface name %q: %w", intfName, err)
	}
	fileName := filepath.Join(procSysNetIPv6ConfPath, intfName, "addr_gen_mode")
	file, err := os.OpenFile(fileName, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("error opening file: %w", err)
	}
	value := "1"
	if generateEUI {
		value = "0"
	}
	if _, err := fmt.Fprintf(file, "%s\n", value); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return fmt.Errorf("error writing to file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("error closing file: %w", err)
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

// parseRoutes extracts routes from a netplan device YAML.
func parseRoutes(device netplan.Device) ([]routeConfig, error) {
	var cfg struct {
		Routes []routeConfig `yaml:"routes"`
	}
	if err := yaml.Unmarshal(device.Raw, &cfg); err != nil {
		return nil, fmt.Errorf("error unmarshalling routes config: %w", err)
	}
	return cfg.Routes, nil
}

// reconcileRoutes adds/replaces desired routes on a link and removes stale
// HBN-managed routes (identified by protocol rtprotHBN) that are no longer desired.
func reconcileRoutes(link netlink.Link, device netplan.Device) error {
	desired, err := parseRoutes(device)
	if err != nil {
		return fmt.Errorf("error parsing routes: %w", err)
	}

	nlRoutes := make([]netlink.Route, 0, len(desired))
	for _, r := range desired {
		nlRoute, err := toNetlinkRoute(link.Attrs().Index, r)
		if err != nil {
			return fmt.Errorf("error converting route {to: %s, via: %s}: %w", r.To, r.Via, err)
		}
		nlRoutes = append(nlRoutes, *nlRoute)
	}

	for i := range nlRoutes {
		if err := netlink.RouteReplace(&nlRoutes[i]); err != nil {
			return fmt.Errorf("error adding route to %s via %s: %w", nlRoutes[i].Dst, nlRoutes[i].Gw, err)
		}
	}

	if err := removeStaleRoutes(link, nlRoutes, unix.AF_INET); err != nil {
		return fmt.Errorf("error removing stale IPv4 routes: %w", err)
	}
	if err := removeStaleRoutes(link, nlRoutes, unix.AF_INET6); err != nil {
		return fmt.Errorf("error removing stale IPv6 routes: %w", err)
	}

	return nil
}

// toNetlinkRoute converts a netplan route config to a netlink.Route.
func toNetlinkRoute(linkIndex int, r routeConfig) (*netlink.Route, error) {
	gw := net.ParseIP(r.Via)
	if gw == nil {
		return nil, fmt.Errorf("invalid gateway %q", r.Via)
	}

	route := &netlink.Route{
		LinkIndex: linkIndex,
		Gw:        gw,
		Protocol:  rtprotHBN,
	}

	if r.To == "default" {
		if gw.To4() != nil {
			_, dst, _ := net.ParseCIDR("0.0.0.0/0")
			route.Dst = dst
		} else {
			_, dst, _ := net.ParseCIDR("::/0")
			route.Dst = dst
		}
	} else {
		_, dst, err := net.ParseCIDR(r.To)
		if err != nil {
			return nil, fmt.Errorf("invalid destination %q: %w", r.To, err)
		}
		route.Dst = dst
	}

	return route, nil
}

// removeStaleRoutes deletes routes with protocol rtprotHBN on the link
// that are not in the desired list.
func removeStaleRoutes(link netlink.Link, desired []netlink.Route, family int) error {
	existing, err := netlink.RouteList(link, family)
	if err != nil {
		return fmt.Errorf("error listing routes: %w", err)
	}
	for i := range existing {
		if existing[i].Protocol != rtprotHBN {
			continue
		}
		if !isDesiredRoute(&existing[i], desired) {
			if err := netlink.RouteDel(&existing[i]); err != nil {
				return fmt.Errorf("error deleting route to %s: %w", existing[i].Dst, err)
			}
		}
	}
	return nil
}

func isDesiredRoute(candidate *netlink.Route, desired []netlink.Route) bool {
	for i := range desired {
		if candidate.LinkIndex != desired[i].LinkIndex {
			continue
		}
		if !candidate.Gw.Equal(desired[i].Gw) {
			continue
		}
		if candidate.Dst == nil && desired[i].Dst == nil {
			return true
		}
		if candidate.Dst != nil && desired[i].Dst != nil && candidate.Dst.String() == desired[i].Dst.String() {
			return true
		}
	}
	return false
}
