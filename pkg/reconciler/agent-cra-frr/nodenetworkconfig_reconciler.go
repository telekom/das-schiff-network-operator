package agent_cra_frr //nolint:revive

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	cra "github.com/telekom/das-schiff-network-operator/pkg/cra-frr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/common"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	baseConfigPath  = "/etc/cra/config/base-config.yaml"
	frrTemplatePath = "/opt/network-operator/frr.conf.tpl"
)

// CRAFRRConfigApplier implements the common.ConfigApplier interface for CRA-FRR.
type CRAFRRConfigApplier struct {
	craManager  *cra.Manager
	baseConfig  *config.BaseConfig
	frrTemplate cra.FRRTemplate
}

// ApplyConfig applies the network configuration using CRA-FRR manager.
func (a *CRAFRRConfigApplier) ApplyConfig(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig) error {
	netlinkConfig := a.convertNodeConfigToNetlink(cfg)
	policyRoutes := convertPolicyRoutes(cfg)

	frrConfig, err := a.frrTemplate.TemplateFRR(a.baseConfig, &cfg.Spec)
	if err != nil {
		return fmt.Errorf("error templating FRR configuration: %w", err)
	}

	if err := a.craManager.ApplyConfiguration(ctx, &netlinkConfig, frrConfig, policyRoutes); err != nil {
		return fmt.Errorf("error applying cra configuration: %w", err)
	}

	return nil
}

func (a *CRAFRRConfigApplier) convertNodeConfigToNetlink(nodeCfg *v1alpha1.NodeNetworkConfig) (netlinkConfig nl.NetlinkConfiguration) {
	for _, layer2 := range nodeCfg.Spec.Layer2s {
		nlLayer2 := nl.Layer2Information{
			VlanID:     int(layer2.VLAN),
			MTU:        int(layer2.MTU),
			VNI:        int(layer2.VNI),
			AnycastMAC: new(string),
		}

		if layer2.IRB != nil {
			nlLayer2.AnycastGateways = layer2.IRB.IPAddresses
			*nlLayer2.AnycastMAC = layer2.IRB.MACAddress
			nlLayer2.VRF = layer2.IRB.VRF
		}

		netlinkConfig.Layer2s = append(netlinkConfig.Layer2s, nlLayer2)

		source := nl.MirrorSourceL2(int(layer2.VLAN))
		for j := range layer2.MirrorACLs {
			netlinkConfig.Mirrors = append(netlinkConfig.Mirrors, convertMirrorACL(&layer2.MirrorACLs[j], source, true))
		}
	}

	// Skip adding management VRF
	for name := range nodeCfg.Spec.FabricVRFs {
		if name == a.baseConfig.ManagementVRF.Name {
			continue
		}

		vrf := nodeCfg.Spec.FabricVRFs[name]
		nlVrf := nl.VRFInformation{
			Name: name,
			VNI:  int(vrf.VNI),
			MTU:  nl.DefaultMtu,
		}

		netlinkConfig.VRFs = append(netlinkConfig.VRFs, nlVrf)

		appendMirrorVRFConfig(&netlinkConfig, name, &vrf)
	}

	for name := range nodeCfg.Spec.LocalVRFs {
		nlVrf := nl.VRFInformation{
			Name:      name,
			MTU:       nl.DefaultMtu,
			LocalOnly: true,
		}
		netlinkConfig.VRFs = append(netlinkConfig.VRFs, nlVrf)
	}

	return netlinkConfig
}

// appendMirrorVRFConfig adds the GRE tunnels, loopbacks and mirror rules carried by
// a fabric VRF to the netlink configuration.
func appendMirrorVRFConfig(netlinkConfig *nl.NetlinkConfiguration, vrfName string, vrf *v1alpha1.FabricVRF) {
	for loName := range vrf.Loopbacks {
		netlinkConfig.Loopbacks = append(netlinkConfig.Loopbacks, nl.LoopbackConfig{
			Name:      loName,
			VRF:       vrfName,
			Addresses: vrf.Loopbacks[loName].IPAddresses,
		})
	}

	for greName := range vrf.GREs {
		gre := vrf.GREs[greName]
		netlinkConfig.GRETunnels = append(netlinkConfig.GRETunnels, nl.GRETunnel{
			Name:            greName,
			VRF:             vrfName,
			Local:           gre.SourceAddress,
			Remote:          gre.DestinationAddress,
			SourceInterface: gre.SourceInterface,
			Key:             gre.EncapsulationKey,
			Layer2:          gre.Layer == v1alpha1.GRELayer2,
		})
	}

	source := nl.MirrorSourceVRF(vrfName)
	for j := range vrf.MirrorACLs {
		netlinkConfig.Mirrors = append(netlinkConfig.Mirrors, convertMirrorACL(&vrf.MirrorACLs[j], source, false))
	}
}

// convertMirrorACL maps a NodeNetworkConfig MirrorACL to a netlink MirrorRule for
// the given source interface. workloadFacing is true for the Layer2 access port
// (vlan.<id>), whose tc hooks are inverted relative to the workload direction.
func convertMirrorACL(acl *v1alpha1.MirrorACL, sourceIface string, workloadFacing bool) nl.MirrorRule {
	direction := string(acl.Direction)
	if direction == "" {
		direction = "both"
	}
	rule := nl.MirrorRule{
		SourceInterface: sourceIface,
		Direction:       direction,
		GREInterface:    acl.MirrorDestination,
		WorkloadFacing:  workloadFacing,
	}
	if acl.TrafficMatch.Protocol != nil {
		rule.Protocol = *acl.TrafficMatch.Protocol
	}
	if acl.TrafficMatch.SrcPrefix != nil {
		rule.SrcPrefix = *acl.TrafficMatch.SrcPrefix
	}
	if acl.TrafficMatch.DstPrefix != nil {
		rule.DstPrefix = *acl.TrafficMatch.DstPrefix
	}
	if acl.TrafficMatch.SrcPort != nil {
		rule.SrcPort = *acl.TrafficMatch.SrcPort
	}
	if acl.TrafficMatch.DstPort != nil {
		rule.DstPort = *acl.TrafficMatch.DstPort
	}
	return rule
}

func convertPolicyRoutes(nodeCfg *v1alpha1.NodeNetworkConfig) []cra.PolicyRoute {
	if nodeCfg.Spec.ClusterVRF == nil {
		return nil
	}

	var routes []cra.PolicyRoute
	for _, pr := range nodeCfg.Spec.ClusterVRF.PolicyRoutes {
		if pr.NextHop.Vrf == nil {
			// ip rules require a VRF to resolve the routing table —
			// address-only next hops are not supported for policy routes.
			continue
		}
		route := cra.PolicyRoute{
			SrcPrefix: pr.TrafficMatch.SrcPrefix,
			DstPrefix: pr.TrafficMatch.DstPrefix,
			SrcPort:   pr.TrafficMatch.SrcPort,
			DstPort:   pr.TrafficMatch.DstPort,
			Protocol:  pr.TrafficMatch.Protocol,
			Vrf:       *pr.NextHop.Vrf,
		}
		routes = append(routes, route)
	}
	return routes
}

// NodeNetworkConfigReconciler wraps the common reconciler with CRA-FRR specific logic.
type NodeNetworkConfigReconciler struct {
	*common.NodeNetworkConfigReconciler
}

// NewNodeNetworkConfigReconciler creates a new NodeNetworkConfigReconciler for CRA-FRR.
func NewNodeNetworkConfigReconciler(
	craManager *cra.Manager,
	clusterClient client.Client,
	logger logr.Logger,
	nodeNetworkConfigPath string,
) (*NodeNetworkConfigReconciler, error) {
	baseConfig, err := config.LoadBaseConfig(baseConfigPath)
	if err != nil {
		return nil, fmt.Errorf("error loading base config: %w", err)
	}

	configApplier := &CRAFRRConfigApplier{
		craManager:  craManager,
		baseConfig:  baseConfig,
		frrTemplate: cra.FRRTemplate{FRRTemplatePath: frrTemplatePath},
	}

	commonReconciler, err := common.NewNodeNetworkConfigReconciler(
		clusterClient,
		logger,
		configApplier,
		nodeNetworkConfigPath,
		common.ReconcilerOptions{
			RestoreOnReconcileFailure: true, // FRR can partially apply invalid configs
		},
	)
	if err != nil {
		return nil, fmt.Errorf("error creating common reconciler: %w", err)
	}

	return &NodeNetworkConfigReconciler{
		NodeNetworkConfigReconciler: commonReconciler,
	}, nil
}
