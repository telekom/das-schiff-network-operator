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
	"github.com/telekom/das-schiff-network-operator/pkg/agent"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nodeconfig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultDebounceTime     = 20 * time.Second
	defaultNodeDebounceTime = 5 * time.Second
	defaultTimeout          = 30 * time.Second

	DefaultNodeConfigPath = "/opt/network-operator/nodeConfig.yaml"
	nodeConfigFilePerm    = 0o600
)

type Reconciler struct {
	client         client.Client
	logger         logr.Logger
	healthChecker  *healthcheck.HealthChecker
	nodeConfig     *v1alpha1.NodeConfig
	nodeConfigPath string
	agentClient    agent.Client

	debouncer *debounce.Debouncer
}

type reconcile struct {
	*Reconciler
	logr.Logger
}

func NewReconciler(clusterClient client.Client, logger logr.Logger, nodeConfigPath string, agentClient agent.Client) (*Reconciler, error) {
	reconciler := &Reconciler{
		client:         clusterClient,
		logger:         logger,
		nodeConfigPath: nodeConfigPath,
		agentClient:    agentClient,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.reconcileDebounced, defaultNodeDebounceTime, logger)

	nc, err := healthcheck.LoadConfig(healthcheck.NetHealthcheckFile)
	if err != nil {
		return nil, fmt.Errorf("error loading networking healthcheck config: %w", err)
	}

	tcpDialer := healthcheck.NewTCPDialer(nc.Timeout)
	reconciler.healthChecker, err = healthcheck.NewHealthChecker(reconciler.client,
		healthcheck.NewDefaultHealthcheckToolkit(nil, tcpDialer),
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

func (r *Reconciler) Reconcile(ctx context.Context) {
	r.debouncer.Debounce(ctx)
}

func (r *Reconciler) reconcileDebounced(ctx context.Context) error {
	reconciler := &reconcile{
		Reconciler: r,
		Logger:     r.logger,
	}

	// get NodeConfig from apiserver
	cfg, err := reconciler.fetchNodeConfig(ctx)
	if err != nil {
		// discard IsNotFound error
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// config is invalid or was already provisioned - discard
	if cfg.Status.ConfigStatus != nodeconfig.StatusProvisioning {
		return nil
	}

	// reconcile config
	if err = reconciler.sendConfig(ctx, cfg); err != nil {
		// if reconciliation failed set NodeConfig's status as invalid and restore last known working config
		if err := reconciler.invalidateAndRestore(ctx, cfg); err != nil {
			return fmt.Errorf("reconciler restoring config: %w", err)
		}

		return fmt.Errorf("reconciler error: %w", err)
	}

	// check if node is healthly after reconciliation
	if err := r.checkHealth(ctx); err != nil {
		// if node is not healthly set NodeConfig's status as invalid and restore last known working config
		if err := reconciler.invalidateAndRestore(ctx, cfg); err != nil {
			return fmt.Errorf("reconciler restoring config: %w", err)
		}

		return fmt.Errorf("healthcheck error (previous config restored): %w", err)
	}

	// set config status as provisioned (valid)
	cfg.Status.ConfigStatus = nodeconfig.StatusProvisioned
	if err = reconciler.client.Status().Update(ctx, cfg); err != nil {
		return fmt.Errorf("error updating NodeConfig status: %w", err)
	}

	// replace in-memory working config and store it on the disk
	r.nodeConfig = cfg
	if err = storeNodeConfig(cfg, reconciler.nodeConfigPath); err != nil {
		return fmt.Errorf("error saving NodeConfig status: %w", err)
	}

	return nil
}

func (r *reconcile) fetchNodeConfig(ctx context.Context) (*v1alpha1.NodeConfig, error) {
	cfg := &v1alpha1.NodeConfig{}
	err := r.client.Get(ctx, types.NamespacedName{Name: os.Getenv(healthcheck.NodenameEnv)}, cfg)
	if err != nil {
		return nil, fmt.Errorf("error getting NodeConfig: %w", err)
	}
	return cfg, nil
}

func (r *reconcile) invalidateAndRestore(ctx context.Context, cfg *v1alpha1.NodeConfig) error {
	cfg.Status.ConfigStatus = nodeconfig.StatusInvalid
	if err := r.client.Status().Update(ctx, cfg); err != nil {
		return fmt.Errorf("error updating NodeConfig status: %w", err)
	}

	// try to restore previously known good NodeConfig
	if err := r.restoreNodeConfig(ctx); err != nil {
		return fmt.Errorf("error restoring NodeConfig: %w", err)
	}

	return nil
}

func (r *reconcile) sendConfig(ctx context.Context, nodeCfg *v1alpha1.NodeConfig) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	if err := r.agentClient.SendConfig(timeoutCtx, nodeCfg); err != nil {
		return fmt.Errorf("error setting configuration: %w", err)
	}

	return nil
}

func (r *reconcile) restoreNodeConfig(ctx context.Context) error {
	if r.nodeConfig == nil {
		return nil
	}
	if err := r.sendConfig(ctx, r.nodeConfig); err != nil {
		return fmt.Errorf("error restoring configuration: %w", err)
	}

	r.logger.Info("restored last known valid config")

	return nil
}

func readNodeConfig(path string) (*v1alpha1.NodeConfig, error) {
	cfg, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading NodeConfig: %w", err)
	}

	nodeConfig := &v1alpha1.NodeConfig{}
	if err := json.Unmarshal(cfg, nodeConfig); err != nil {
		return nil, fmt.Errorf("error unmarshalling NodeConfig: %w", err)
	}

	return nodeConfig, nil
}

func storeNodeConfig(cfg *v1alpha1.NodeConfig, path string) error {
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

func (r *Reconciler) checkHealth(ctx context.Context) error {
	if err := r.healthChecker.CheckReachability(); err != nil {
		return fmt.Errorf("error checking network reachability: %w", err)
	}
	if err := r.healthChecker.CheckAPIServer(ctx); err != nil {
		return fmt.Errorf("error checking API Server reachability: %w", err)
	}
	if !r.healthChecker.TaintsRemoved() {
		if err := r.healthChecker.RemoveTaints(ctx); err != nil {
			return fmt.Errorf("error removing taint from the node: %w", err)
		}
	}
	return nil
}
