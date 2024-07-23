package reconciler

import (
	"context"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type LegacyReconciler struct {
	netlinkManager *nl.Manager
	config         *config.Config
	frrManager     *frr.Manager
	anycastTracker *anycast.Tracker
	dirtyFRRConfig bool
	healthChecker  *healthcheck.HealthChecker
	logger         logr.Logger
}

func NewLegacyReconciler(clusterClient client.Client, anycastTracker *anycast.Tracker, logger logr.Logger) (*LegacyReconciler, error) {
	reconciler := &LegacyReconciler{
		netlinkManager: &nl.Manager{},
		frrManager:     frr.NewFRRManager(),
		anycastTracker: anycastTracker,
		logger:         logger,
	}

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
	reconciler.healthChecker, err = healthcheck.NewHealthChecker(clusterClient,
		healthcheck.NewDefaultHealthcheckToolkit(reconciler.frrManager, tcpDialer),
		nc)
	if err != nil {
		return nil, fmt.Errorf("error creating netwokring healthchecker: %w", err)
	}

	return reconciler, nil
}

func (r *LegacyReconciler) checkHealth(ctx context.Context) error {
	if !r.healthChecker.IsNetworkingHealthy() {
		_, err := r.healthChecker.IsFRRActive()
		if err != nil {
			return fmt.Errorf("error checking FRR status: %w", err)
		}
		if err = r.healthChecker.CheckInterfaces(); err != nil {
			return fmt.Errorf("error checking network interfaces: %w", err)
		}
		if err = r.healthChecker.CheckReachability(); err != nil {
			return fmt.Errorf("error checking network reachability: %w", err)
		}
		if err = r.healthChecker.RemoveTaints(ctx); err != nil {
			return fmt.Errorf("error removing taint from the node: %w", err)
		}
	}
	return nil
}

func (r *LegacyReconciler) getConfig() *config.Config {
	return r.config
}
