package agent_cra_frr //nolint:revive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/cra"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DefaultNodeNetworkConfigPath = "/opt/network-operator/current-config.yaml"
	baseConfigPath               = "/etc/cra/config/base-config.yaml"
	frrTemplatePath              = "/opt/network-operator/frr.conf.tpl"
	nodeNetworkConfigFilePerm    = 0o600
)

type NodeNetworkConfigReconciler struct {
	client                client.Client
	craManager            *cra.Manager
	baseConfig            *config.BaseConfig
	frrTemplate           cra.FRRTemplate
	logger                logr.Logger
	healthChecker         *healthcheck.HealthChecker
	NodeNetworkConfig     *v1alpha1.NodeNetworkConfig
	NodeNetworkConfigPath string
}

type reconcileNodeNetworkConfig struct {
	*NodeNetworkConfigReconciler
	logr.Logger
}

func NewNodeNetworkConfigReconciler(clusterClient client.Client, logger logr.Logger, nodeNetworkConfigPath string) (*NodeNetworkConfigReconciler, error) {
	reconciler := &NodeNetworkConfigReconciler{
		client:                clusterClient,
		logger:                logger,
		NodeNetworkConfigPath: nodeNetworkConfigPath,
	}

	craManager, err := cra.NewManager(os.Getenv("CRA_URL"), os.Getenv("CRA_CLIENT_CERT"), os.Getenv("CRA_CLIENT_KEY"))
	if err != nil {
		return nil, fmt.Errorf("error creating CRA manager: %w", err)
	}
	reconciler.craManager = craManager

	baseConfig, err := config.LoadBaseConfig(baseConfigPath)
	if err != nil {
		return nil, fmt.Errorf("error loading base config: %w", err)
	}
	reconciler.baseConfig = baseConfig

	reconciler.frrTemplate = cra.FRRTemplate{FRRTemplatePath: frrTemplatePath}

	nc, err := healthcheck.LoadConfig(healthcheck.NetHealthcheckFile)
	if err != nil {
		return nil, fmt.Errorf("error loading networking healthcheck config: %w", err)
	}

	tcpDialer := healthcheck.NewTCPDialer(nc.Timeout)
	reconciler.healthChecker, err = healthcheck.NewHealthChecker(reconciler.client,
		healthcheck.NewDefaultHealthcheckToolkit(tcpDialer),
		nc)
	if err != nil {
		return nil, fmt.Errorf("error creating networking healthchecker: %w", err)
	}

	reconciler.NodeNetworkConfig, err = readNodeNetworkConfig(reconciler.NodeNetworkConfigPath)
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("error reading NodeNetworkConfig from disk: %w", err)
	}

	return reconciler, nil
}

func (r *reconcileNodeNetworkConfig) fetchNodeConfig(ctx context.Context) (*v1alpha1.NodeNetworkConfig, error) {
	cfg := &v1alpha1.NodeNetworkConfig{}
	err := r.client.Get(ctx, types.NamespacedName{Name: os.Getenv(healthcheck.NodenameEnv)}, cfg)
	if err != nil {
		return nil, fmt.Errorf("error getting NodeConfig: %w", err)
	}
	return cfg, nil
}

func (reconciler *NodeNetworkConfigReconciler) Reconcile(ctx context.Context) error {
	r := &reconcileNodeNetworkConfig{
		NodeNetworkConfigReconciler: reconciler,
		Logger:                      reconciler.logger,
	}

	// get NodeNetworkConfig from apiserver
	cfg, err := r.fetchNodeConfig(ctx)
	if err != nil {
		// discard IsNotFound error
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if r.NodeNetworkConfig != nil && r.NodeNetworkConfig.Spec.Revision == cfg.Spec.Revision {
		// replace in-memory working NodeNetworkConfig and store it on the disk
		if err := reconciler.storeConfig(cfg, reconciler.NodeNetworkConfigPath); err != nil {
			return fmt.Errorf("error saving NodeNetworkConfig status: %w", err)
		}

		// current in-memory conifg has the same revision as the fetched one
		// this means that NodeNetworkConfig was already provisioned - skip
		if cfg.Status.ConfigStatus != operator.StatusProvisioned {
			if err := setStatus(ctx, r.client, cfg, operator.StatusProvisioned, r.logger); err != nil {
				return fmt.Errorf("error setting NodeNetworkConfig status: %w", err)
			}
		}
		return nil
	}

	// NodeNetworkConfig is invalid - discard
	if cfg.Status.ConfigStatus == operator.StatusInvalid {
		r.logger.Info("skipping invalid NodeNetworkConfig", "name", cfg.Name)
		return nil
	}
	if err := r.processConfig(ctx, cfg); err != nil {
		return fmt.Errorf("error while processing NodeNetworkConfig: %w", err)
	}

	// replace in-memory working NodeNetworkConfig and store it on the disk
	if err := reconciler.storeConfig(cfg, reconciler.NodeNetworkConfigPath); err != nil {
		return fmt.Errorf("error saving NodeNetworkConfig status: %w", err)
	}

	return nil
}

func (r *reconcileNodeNetworkConfig) processConfig(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig) error {
	// set NodeNetworkConfig status as provisioning
	if err := setStatus(ctx, r.client, cfg, operator.StatusProvisioning, r.logger); err != nil {
		return fmt.Errorf("error setting NodeNetworkConfig status %s: %w", operator.StatusProvisioning, err)
	}

	// reconcile NodeNetworkConfig
	if err := r.doReconciliation(ctx, cfg); err != nil {
		// if reconciliation failed set NodeNetworkConfig's status as invalid and restore last known working NodeNetworkConfig
		if err := r.invalidateAndRestore(ctx, cfg, "reconciliation failed"); err != nil {
			return fmt.Errorf("reconciler restoring NodeNetworkConfig: %w", err)
		}

		return fmt.Errorf("reconciler error: %w", err)
	}

	// check if node is healthly after reconciliation
	if err := r.checkHealth(ctx); err != nil {
		// if node is not healthly set NodeNetworkConfig's status as invalid and restore last known working NodeNetworkConfig
		if err := r.invalidateAndRestore(ctx, cfg, "healthcheck failed"); err != nil {
			return fmt.Errorf("failed to restore NodeNetworkConfig: %w", err)
		}

		return fmt.Errorf("healthcheck error (previous NodeNetworkConfig restored): %w", err)
	}

	// set NodeNetworkConfig status as provisioned (valid)
	if err := setStatus(ctx, r.client, cfg, operator.StatusProvisioned, r.logger); err != nil {
		return fmt.Errorf("error setting NodeNetworkConfig status %s: %w", operator.StatusProvisioned, err)
	}

	return nil
}

func setStatus(ctx context.Context, c client.Client, cfg *v1alpha1.NodeNetworkConfig, status string, logger logr.Logger) error {
	logger.Info("setting NodeNetworkConfig status", "name", cfg.Name, "status", status)
	cfg.Status.ConfigStatus = status
	cfg.Status.LastUpdate = metav1.Now()
	if err := c.Status().Update(ctx, cfg); err != nil {
		return fmt.Errorf("error updating NodeNetworkConfig status: %w", err)
	}
	return nil
}

func (r *reconcileNodeNetworkConfig) invalidateAndRestore(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig, reason string) error {
	r.logger.Info("invalidating NodeNetworkConfig", "name", cfg.Name, "reason", reason)
	if err := setStatus(ctx, r.client, cfg, operator.StatusInvalid, r.logger); err != nil {
		return fmt.Errorf("error invalidating NodeNetworkConfig: %w", err)
	}

	// try to restore previously known good NodeNetworkConfig
	r.logger.Info("restoring previous NodeNetworkConfig")
	if err := r.restoreNodeNetworkConfig(ctx); err != nil {
		return fmt.Errorf("error restoring NodeNetworkConfig: %w", err)
	}

	return nil
}

func (r *reconcileNodeNetworkConfig) doReconciliation(ctx context.Context, nodeCfg *v1alpha1.NodeNetworkConfig) error {
	r.logger.Info("config to reconcile", "NodeNetworkConfig", *nodeCfg)

	netlinkConfig := ConvertNodeConfigToNetlink(nodeCfg)

	frrConfig, err := r.frrTemplate.TemplateFRR(r.baseConfig, &nodeCfg.Spec)
	if err != nil {
		return fmt.Errorf("error templating FRR configuration: %w", err)
	}

	if err := r.craManager.ApplyConfiguration(ctx, &netlinkConfig, frrConfig); err != nil {
		return fmt.Errorf("error applying cra configuration: %w", err)
	}

	return nil
}

func (r *reconcileNodeNetworkConfig) restoreNodeNetworkConfig(ctx context.Context) error {
	if r.NodeNetworkConfig == nil {
		return nil
	}
	if err := r.doReconciliation(ctx, r.NodeNetworkConfig); err != nil {
		return fmt.Errorf("error restoring NodeNetworkConfig: %w", err)
	}

	r.logger.Info("restored last known valid NodeNetworkConfig")

	return nil
}

func readNodeNetworkConfig(path string) (*v1alpha1.NodeNetworkConfig, error) {
	cfg, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading NodeNetworkConfig: %w", err)
	}

	nodeNetworkConfig := &v1alpha1.NodeNetworkConfig{}
	if err := json.Unmarshal(cfg, nodeNetworkConfig); err != nil {
		return nil, fmt.Errorf("error unmarshalling NodeNetworkConfig: %w", err)
	}

	return nodeNetworkConfig, nil
}

func (reconciler *NodeNetworkConfigReconciler) storeConfig(cfg *v1alpha1.NodeNetworkConfig, path string) error {
	reconciler.NodeNetworkConfig = cfg
	// save working NodeNetworkConfig
	c, err := json.MarshalIndent(*reconciler.NodeNetworkConfig, "", " ")
	if err != nil {
		panic(err)
	}

	if err = os.WriteFile(path, c, nodeNetworkConfigFilePerm); err != nil {
		return fmt.Errorf("error saving NodeNetworkConfig status: %w", err)
	}

	return nil
}

func (reconciler *NodeNetworkConfigReconciler) checkHealth(ctx context.Context) error {
	if err := reconciler.healthChecker.CheckInterfaces(); err != nil {
		return fmt.Errorf("error checking network interfaces: %w", err)
	}
	if err := reconciler.healthChecker.CheckReachability(); err != nil {
		return fmt.Errorf("error checking network reachability: %w", err)
	}
	if err := reconciler.healthChecker.CheckAPIServer(ctx); err != nil {
		return fmt.Errorf("error checking API Server reachability: %w", err)
	}
	if !reconciler.healthChecker.TaintsRemoved() {
		if err := reconciler.healthChecker.RemoveTaints(ctx); err != nil {
			return fmt.Errorf("error removing taint from the node: %w", err)
		}
	}
	return nil
}

func ConvertNodeConfigToNetlink(nodeCfg *v1alpha1.NodeNetworkConfig) (netlinkConfig nl.NetlinkConfiguration) {
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

	for name := range nodeCfg.Spec.FabricVRFs {
		nlVrf := nl.VRFInformation{
			Name: name,
			VNI:  int(nodeCfg.Spec.FabricVRFs[name].VNI),
			MTU:  nl.DefaultMtu,
		}

		netlinkConfig.VRFs = append(netlinkConfig.VRFs, nlVrf)
	}
	return netlinkConfig
}
