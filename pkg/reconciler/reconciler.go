package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultDebounceTime = 20 * time.Second
	nodeConfigPath      = "/opt/network-operator/nodeConfig.yaml"
	nodeConfigFilePerm  = 0o600
)

type Reconciler struct {
	client         client.Client
	netlinkManager *nl.NetlinkManager
	frrManager     *frr.Manager
	anycastTracker *anycast.Tracker
	config         *config.Config
	logger         logr.Logger
	healthChecker  *healthcheck.HealthChecker

	debouncer *debounce.Debouncer

	dirtyFRRConfig bool
}

type reconcile struct {
	*Reconciler
	logr.Logger
}

func NewReconciler(clusterClient client.Client, anycastTracker *anycast.Tracker, logger logr.Logger) (*Reconciler, error) {
	reconciler := &Reconciler{
		client:         clusterClient,
		netlinkManager: &nl.NetlinkManager{},
		frrManager:     frr.NewFRRManager(),
		anycastTracker: anycastTracker,
		logger:         logger,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.reconcileDebounced, defaultDebounceTime, logger)

	if val := os.Getenv("FRR_CONFIG_FILE"); val != "" {
		reconciler.frrManager.ConfigPath = val
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

	return reconciler, nil
}

func (reconciler *Reconciler) Reconcile(ctx context.Context) {
	reconciler.debouncer.Debounce(ctx)
}

func (reconciler *Reconciler) reconcileDebounced(ctx context.Context) error {
	r := &reconcile{
		Reconciler: reconciler,
		Logger:     reconciler.logger,
	}

	r.Logger.Info("reloading config")
	if err := r.config.ReloadConfig(); err != nil {
		return fmt.Errorf("error reloading network-operator config: %w", err)
	}

	r.Logger.Info("fetching NodeConfig")
	// get NodeConfig from apiserver
	cfg, err := r.fetchNodeConfig(ctx)
	if err != nil {
		return err
	}

	r.Logger.Info("NodeConfig status", "status", cfg.Status.ConfigStatus)

	// config is invalid or was already provisioned - discard
	if cfg.Status.ConfigStatus == statusInvalid || cfg.Status.ConfigStatus == statusProvisioned {
		r.Logger.Info("NodeConfig discarded with", "status", cfg.Status.ConfigStatus)
		return nil
	}

	// reconcile config
	if err = doReconciliation(r, cfg); err != nil {
		r.Logger.Info("reconcile failed")
		// if reconciliation failed set NodeConfig's status as invalid
		cfg.Status.ConfigStatus = statusInvalid
		if err := r.client.Status().Update(ctx, cfg); err != nil {
			return fmt.Errorf("error updating NodeConfig status: %w", err)
		}

		// try to restore previously known good NodeConfig
		if err := restoreNodeConfig(r); err != nil {
			return fmt.Errorf("error restoring NodeConfig: %w", err)
		}

		return fmt.Errorf("reconciler error: %w", err)
	}

	r.Logger.Info("reconcile successful")

	// check if node is healthly after reconciliation
	if err := reconciler.checkHealth(ctx); err != nil {
		r.logger.Error(err, "checkHealth failed")
		// if node is not healthly set NodeConfig's status as invalid
		cfg.Status.ConfigStatus = statusInvalid
		if err = r.client.Status().Update(ctx, cfg); err != nil {
			return fmt.Errorf("error updating NodeConfig status: %w", err)
		}

		// try to restore previously known good NodeConfig
		if err = restoreNodeConfig(r); err != nil {
			return fmt.Errorf("error restoring NodeConfig: %w", err)
		}

		return fmt.Errorf("healthcheck error (previous config restored): %w", err)
	}

	r.Logger.Info("checkHealth succeeded")

	// set config status as provisioned (valid)
	r.Logger.Info("will set NodeConfig status to provisioned")
	cfg.Status.ConfigStatus = statusProvisioned
	if err = r.client.Status().Update(ctx, cfg); err != nil {
		r.Logger.Info("failed set NodeConfig status to provisioned")
		return fmt.Errorf("error updating NodeConfig status: %w", err)
	}

	r.Logger.Info("will save config to a file")
	// save working config
	c, err := json.MarshalIndent(*cfg, "", " ")
	if err != nil {
		panic(err)
	}

	if err = os.WriteFile(nodeConfigPath, c, nodeConfigFilePerm); err != nil {
		return fmt.Errorf("error saving NodeConfig status: %w", err)
	}

	r.Logger.Info("config stored")

	return nil
}

func doReconciliation(r *reconcile, nodeCfg *v1alpha1.NodeConfig) error {
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

func restoreNodeConfig(r *reconcile) error {
	r.logger.Info("restoring config")
	// config could be stored in memory and be read only on startup
	r.logger.Info("reading data")
	cfg, err := os.ReadFile(nodeConfigPath)
	if err != nil {
		return fmt.Errorf("error reading NodeConfig: %w", err)
	}

	r.logger.Info("unmarshalling data")
	nodeCfg := &v1alpha1.NodeConfig{}
	if err := json.Unmarshal(cfg, nodeCfg); err != nil {
		return fmt.Errorf("error unmarshalling NodeConfig: %w", err)
	}

	r.logger.Info("doReconciliation")
	if err = doReconciliation(r, nodeCfg); err != nil {
		return fmt.Errorf("error restroing configuration: %w", err)
	}

	r.logger.Info("config restored")

	return nil
}

func (reconciler *Reconciler) checkHealth(ctx context.Context) error {
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
	if err := reconciler.healthChecker.RemoveTaints(ctx); err != nil {
		return fmt.Errorf("error removing taint from the node: %w", err)
	}
	return nil
}
