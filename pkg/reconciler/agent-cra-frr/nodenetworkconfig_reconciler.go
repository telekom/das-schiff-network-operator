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
	// Pre-compute loopback IPs per VRF for GRE local address resolution.
	vrfLoopbackIPs := make(map[string]string)
	for name, vrf := range nodeCfg.Spec.FabricVRFs {
		for _, lo := range vrf.Loopbacks {
			if len(lo.IPAddresses) > 0 {
				vrfLoopbackIPs[name] = lo.IPAddresses[0]
				break
			}
		}
	}

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

		// Convert L2 MirrorACLs → mirror rules targeting the bridge interface
		bridgeIface := fmt.Sprintf("br.%d", layer2.VLAN)
		for _, acl := range layer2.MirrorACLs {
			netlinkConfig.Mirrors = append(netlinkConfig.Mirrors,
				convertMirrorACL(acl, bridgeIface, vrfLoopbackIPs))
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

		// Convert VRF MirrorACLs → mirror rules targeting the VRF device
		for _, acl := range vrf.MirrorACLs {
			netlinkConfig.Mirrors = append(netlinkConfig.Mirrors,
				convertMirrorACL(acl, name, vrfLoopbackIPs))
		}

		// Convert VRF loopbacks → loopback configs for netlink dummy creation
		for loName, lo := range vrf.Loopbacks {
			netlinkConfig.Loopbacks = append(netlinkConfig.Loopbacks, nl.LoopbackConfig{
				Name:      "lo." + loName,
				VRF:       name,
				Addresses: lo.IPAddresses,
			})
		}
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

// convertMirrorACL converts a single NNC MirrorACL to an nl.MirrorRule.
// vrfLoopbackIPs maps VRF name → first loopback IP for GRE local address.
func convertMirrorACL(acl v1alpha1.MirrorACL, sourceIface string, vrfLoopbackIPs map[string]string) nl.MirrorRule {
	rule := nl.MirrorRule{
		SourceInterface: sourceIface,
		Direction:       "both",
		GRERemote:       acl.DestinationAddress,
		GRELocal:        vrfLoopbackIPs[acl.DestinationVrf],
		GREVRF:          acl.DestinationVrf,
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
