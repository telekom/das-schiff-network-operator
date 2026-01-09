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

	frrConfig, err := a.frrTemplate.TemplateFRR(a.baseConfig, &cfg.Spec)
	if err != nil {
		return fmt.Errorf("error templating FRR configuration: %w", err)
	}

	if err := a.craManager.ApplyConfiguration(ctx, &netlinkConfig, frrConfig); err != nil {
		return fmt.Errorf("error applying cra configuration: %w", err)
	}

	return nil
}

func (a *CRAFRRConfigApplier) convertNodeConfigToNetlink(nodeCfg *v1alpha1.NodeNetworkConfig) (netlinkConfig nl.NetlinkConfiguration) {
	for _, layer2 := range nodeCfg.Spec.Layer2s {
		neighSuppression := false

		nlLayer2 := nl.Layer2Information{
			VlanID:           int(layer2.VLAN),
			MTU:              int(layer2.MTU),
			VNI:              int(layer2.VNI),
			NeighSuppression: &neighSuppression,
			AnycastMAC:       new(string),
		}

		if layer2.IRB != nil {
			nlLayer2.AnycastGateways = layer2.IRB.IPAddresses
			*nlLayer2.AnycastMAC = layer2.IRB.MACAddress
			nlLayer2.VRF = layer2.IRB.VRF
		}

		netlinkConfig.Layer2s = append(netlinkConfig.Layer2s, nlLayer2)
	}

	// Skip adding management VRF
	for name := range nodeCfg.Spec.FabricVRFs {
		if name == a.baseConfig.ManagementVRF.Name {
			continue
		}

		nlVrf := nl.VRFInformation{
			Name: name,
			VNI:  int(nodeCfg.Spec.FabricVRFs[name].VNI),
			MTU:  nl.DefaultMtu,
		}

		netlinkConfig.VRFs = append(netlinkConfig.VRFs, nlVrf)
	}
	return netlinkConfig
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
	)
	if err != nil {
		return nil, fmt.Errorf("error creating common reconciler: %w", err)
	}

	return &NodeNetworkConfigReconciler{
		NodeNetworkConfigReconciler: commonReconciler,
	}, nil
}
