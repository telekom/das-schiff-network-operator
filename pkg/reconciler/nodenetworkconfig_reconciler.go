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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultDebounceTime = 1 * time.Second

	DefaultNodeNetworkConfigPath = "/opt/network-operator/current-config.yaml"
	NodeNetworkConfigFilePerm    = 0o600
)

type NodeNetworkConfigReconciler struct {
	client                client.Client
	netlinkManager        *nl.Manager
	frrManager            frr.ManagerInterface
	anycastTracker        *anycast.Tracker
	config                *config.Config
	logger                logr.Logger
	healthChecker         *healthcheck.HealthChecker
	NodeNetworkConfig     *v1alpha1.NodeNetworkConfig
	NodeNetworkConfigPath string
	dirtyFRRConfig        bool
}

type reconcileNodeNetworkConfig struct {
	*NodeNetworkConfigReconciler
	logr.Logger
}

func NewNodeNetworkConfigReconciler(clusterClient client.Client, anycastTracker *anycast.Tracker, logger logr.Logger, nodeNetworkConfigPath string, frrManager frr.ManagerInterface, netlinkManager *nl.Manager) (*NodeNetworkConfigReconciler, error) {
	reconciler := &NodeNetworkConfigReconciler{
		client:                clusterClient,
		netlinkManager:        netlinkManager,
		frrManager:            frrManager,
		anycastTracker:        anycastTracker,
		logger:                logger,
		NodeNetworkConfigPath: nodeNetworkConfigPath,
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	reconciler.config = cfg

	if val := os.Getenv("FRR_CONFIG_FILE"); val != "" {
		reconciler.frrManager.SetConfigPath(val)
	}

	if err := reconciler.frrManager.Init(cfg.SkipVRFConfig[0]); err != nil {
		return nil, fmt.Errorf("error trying to init FRR Manager: %w", err)
	}

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

	reconciler.NodeNetworkConfig, err = readNodeNetworkConfig(reconciler.NodeNetworkConfigPath)
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("error reading NodeNetworkConfig from disk: %w", err)
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
		if cfg.Status.ConfigStatus != StatusProvisioned {
			if err := setStatus(ctx, r.client, cfg, StatusProvisioned, r.logger); err != nil {
				return fmt.Errorf("error setting NodeNetworkConfig status: %w", err)
			}
		}
		return nil
	}

	// NodeNetworkConfig is invalid - discard
	if cfg.Status.ConfigStatus == StatusInvalid {
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
	if err := setStatus(ctx, r.client, cfg, StatusProvisioning, r.logger); err != nil {
		return fmt.Errorf("error setting NodeNetworkConfig status %s: %w", StatusProvisioning, err)
	}

	// reconcile NodeNetworkConfig
	if err := doReconciliation(r, cfg); err != nil {
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
	if err := setStatus(ctx, r.client, cfg, StatusProvisioned, r.logger); err != nil {
		return fmt.Errorf("error setting NodeNetworkConfig status %s: %w", StatusProvisioned, err)
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
	if err := setStatus(ctx, r.client, cfg, StatusInvalid, r.logger); err != nil {
		return fmt.Errorf("error invalidating NodeNetworkConfig: %w", err)
	}

	// try to restore previously known good NodeNetworkConfig
	r.logger.Info("restoring previous NodeNetworkConfig")
	if err := r.restoreNodeNetworkConfig(); err != nil {
		return fmt.Errorf("error restoring NodeNetworkConfig: %w", err)
	}

	return nil
}

func doReconciliation(r *reconcileNodeNetworkConfig, nodeCfg *v1alpha1.NodeNetworkConfig) error {
	r.logger.Info("config to reconcile", "NodeNetworkConfig", *nodeCfg)
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

func (r *reconcileNodeNetworkConfig) restoreNodeNetworkConfig() error {
	if r.NodeNetworkConfig == nil {
		return nil
	}
	if err := doReconciliation(r, r.NodeNetworkConfig); err != nil {
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

	if err = os.WriteFile(path, c, NodeNetworkConfigFilePerm); err != nil {
		return fmt.Errorf("error saving NodeNetworkConfig status: %w", err)
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
