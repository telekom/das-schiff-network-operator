package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultDebounceTime = 5 * time.Second

	DefaultNodeConfigPath = "/opt/network-operator/nodeConfig.yaml"
	nodeConfigFilePerm    = 0o600
)

type NodeNetworkConfigReconciler struct {
	client         client.Client
	netlinkManager *nl.Manager
	frrManager     frr.ManagerInterface
	anycastTracker *anycast.Tracker
	config         *config.Config
	logger         logr.Logger
	healthChecker  *healthcheck.HealthChecker
	nodeConfig     *v1alpha1.NodeNetworkConfig
	nodeConfigPath string
	dirtyFRRConfig bool
}

type reconcileNodeNetworkConfig struct {
	*NodeNetworkConfigReconciler
	logr.Logger
}

func NewNodeNetworkConfigReconciler(clusterClient client.Client, anycastTracker *anycast.Tracker, logger logr.Logger, nodeConfigPath string, frrManager frr.ManagerInterface, netlinkManager *nl.Manager) (*NodeNetworkConfigReconciler, error) {
	reconciler := &NodeNetworkConfigReconciler{
		client:         clusterClient,
		netlinkManager: netlinkManager,
		frrManager:     frrManager,
		anycastTracker: anycastTracker,
		logger:         logger,
		nodeConfigPath: nodeConfigPath,
	}

	if val := os.Getenv("FRR_CONFIG_FILE"); val != "" {
		reconciler.frrManager.SetConfigPath(val)
	}
	if err := reconciler.frrManager.Init(); err != nil {
		return nil, fmt.Errorf("error trying to init FRR Manager: %w", err)
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	reconciler.config = cfg

	nc, err := healthcheck.LoadConfig(healthcheck.NetHealthcheckFile)
	if err != nil {
		return nil, fmt.Errorf("error loading networking healthcheck config: %w", err)
	}

	tcpDialer := healthcheck.NewTCPDialer(nc.Timeout)
	reconciler.healthChecker, err = healthcheck.NewHealthChecker(reconciler.client,
		healthcheck.NewDefaultHealthcheckToolkit(reconciler.frrManager, tcpDialer),
		nc)
	if err != nil {
		return nil, fmt.Errorf("error creating networking healthchecker: %w", err)
	}

	reconciler.nodeConfig, err = readNodeConfig(reconciler.nodeConfigPath)
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("error reading NodeConfig from disk: %w", err)
	}

	return reconciler, nil
}

func (reconciler *NodeNetworkConfigReconciler) Reconcile(ctx context.Context) error {
	r := &reconcileNodeNetworkConfig{
		NodeNetworkConfigReconciler: reconciler,
		Logger:                      reconciler.logger,
	}

	if err := r.config.ReloadConfig(); err != nil {
		return fmt.Errorf("error reloading network-operator config: %w", err)
	}

	// get NodeConfig from apiserver
	cfg, err := r.fetchNodeConfig(ctx)
	if err != nil {
		// discard IsNotFound error
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if r.nodeConfig != nil && r.nodeConfig.Spec.Revision == cfg.Spec.Revision {
		// current in-memory conifg has the same revision as the fetched one
		// this means that config was already provisioned - skip
		return nil
	}

	// config is invalid - discard
	if cfg.Status.ConfigStatus == StatusInvalid {
		return nil
	}

	if err := r.processConfig(ctx, cfg); err != nil {
		return fmt.Errorf("error while processing config: %w", err)
	}

	// replace in-memory working config and store it on the disk
	reconciler.nodeConfig = cfg
	if err := storeNodeConfig(cfg, reconciler.nodeConfigPath); err != nil {
		return fmt.Errorf("error saving NodeConfig status: %w", err)
	}

	return nil
}

func (r *reconcileNodeNetworkConfig) processConfig(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig) error {
	// set config status as provisioned (valid)
	if err := r.setStatus(ctx, cfg, StatusProvisioning); err != nil {
		return fmt.Errorf("error setting config status %s: %w", StatusProvisioning, err)
	}

	// reconcile config
	if err := doReconciliation(r, cfg); err != nil {
		// if reconciliation failed set NodeConfig's status as invalid and restore last known working config
		if err := r.invalidateAndRestore(ctx, cfg); err != nil {
			return fmt.Errorf("reconciler restoring config: %w", err)
		}

		return fmt.Errorf("reconciler error: %w", err)
	}

	// check if node is healthly after reconciliation
	if err := r.checkHealth(ctx); err != nil {
		// if node is not healthly set NodeConfig's status as invalid and restore last known working config
		if err := r.invalidateAndRestore(ctx, cfg); err != nil {
			return fmt.Errorf("reconciler restoring config: %w", err)
		}

		return fmt.Errorf("healthcheck error (previous config restored): %w", err)
	}

	// set config status as provisioned (valid)
	if err := r.setStatus(ctx, cfg, StatusProvisioned); err != nil {
		return fmt.Errorf("error setting config status %s: %w", StatusProvisioned, err)
	}

	return nil
}

func (r *reconcileNodeNetworkConfig) setStatus(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig, status string) error {
	// set config status as provisioned (valid)
	cfg.Status.ConfigStatus = status
	if err := r.client.Status().Update(ctx, cfg); err != nil {
		return fmt.Errorf("error updating NodeConfig status: %w", err)
	}
	return nil
}

func (r *reconcileNodeNetworkConfig) invalidateAndRestore(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig) error {
	cfg.Status.ConfigStatus = StatusInvalid
	if err := r.client.Status().Update(ctx, cfg); err != nil {
		return fmt.Errorf("error updating NodeConfig status: %w", err)
	}

	// try to restore previously known good NodeConfig
	if err := r.restoreNodeConfig(); err != nil {
		return fmt.Errorf("error restoring NodeConfig: %w", err)
	}

	return nil
}

func doReconciliation(r *reconcileNodeNetworkConfig, nodeCfg *v1alpha1.NodeNetworkConfig) error {
	r.logger.Info("config to reconcile", "NodeConfig", *nodeCfg)
	l3vnis := nodeCfg.Spec.Vrf
	l2vnis := nodeCfg.Spec.Layer2
	taas := nodeCfg.Spec.RoutingTable

	if err := r.reconcileLayer3(l3vnis, taas); err != nil {
		return err
	}
	if err := r.reconcileLayer2(l2vnis); err != nil {
		return err
	}

	return nil
}

func (r *reconcileNodeNetworkConfig) restoreNodeConfig() error {
	if r.nodeConfig == nil {
		return nil
	}
	if err := doReconciliation(r, r.nodeConfig); err != nil {
		return fmt.Errorf("error restoring configuration: %w", err)
	}

	r.logger.Info("restored last known valid config")

	return nil
}

func readNodeConfig(path string) (*v1alpha1.NodeNetworkConfig, error) {
	cfg, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading NodeConfig: %w", err)
	}

	nodeConfig := &v1alpha1.NodeNetworkConfig{}
	if err := json.Unmarshal(cfg, nodeConfig); err != nil {
		return nil, fmt.Errorf("error unmarshalling NodeConfig: %w", err)
	}

	return nodeConfig, nil
}

func storeNodeConfig(cfg *v1alpha1.NodeNetworkConfig, path string) error {
	// save working config
	c, err := json.MarshalIndent(*cfg, "", " ")
	if err != nil {
		panic(err)
	}

	if err = os.WriteFile(path, c, nodeConfigFilePerm); err != nil {
		return fmt.Errorf("error saving NodeConfig status: %w", err)
	}

	return nil
}

func (reconciler *NodeNetworkConfigReconciler) checkHealth(ctx context.Context) error {
	_, err := reconciler.healthChecker.IsFRRActive()
	if err != nil {
		return fmt.Errorf("error checking FRR status: %w", err)
	}
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
