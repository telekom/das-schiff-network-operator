package reconciler

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultDebounceTime = 20 * time.Second

type Reconciler struct {
	client         client.Client
	netlinkManager *nl.Manager
	frrManager     *frr.Manager
	anycastTracker *anycast.Tracker
	config         *config.Config
	logger         logr.Logger
	healthChecker  *healthcheck.HealthChecker

	debouncer *debounce.Debouncer
}

type reconcile struct {
	*Reconciler
	logr.Logger
}

func NewReconciler(clusterClient client.Client, anycastTracker *anycast.Tracker, logger logr.Logger) (*Reconciler, error) {
	reconciler := &Reconciler{
		client:         clusterClient,
		netlinkManager: nl.NewManager(&nl.Toolkit{}),
		frrManager:     frr.NewFRRManager(),
		anycastTracker: anycastTracker,
		logger:         logger,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.reconcileDebounced, defaultDebounceTime, logger)

	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	reconciler.config = cfg

	if val := os.Getenv("FRR_CONFIG_FILE"); val != "" {
		reconciler.frrManager.ConfigPath = val
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
		return nil, fmt.Errorf("error creating netwokring healthchecker: %w", err)
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

	r.Logger.Info("Reloading config")
	if err := r.config.ReloadConfig(); err != nil {
		return fmt.Errorf("error reloading network-operator config: %w", err)
	}

	l3vnis, err := r.fetchLayer3(ctx)
	if err != nil {
		return err
	}
	l2vnis, err := r.fetchLayer2(ctx)
	if err != nil {
		return err
	}
	taas, err := r.fetchTaas(ctx)
	if err != nil {
		return err
	}

	if err := r.reconcileLayer3(l3vnis, taas); err != nil {
		return err
	}
	if err := r.reconcileLayer2(l2vnis); err != nil {
		return err
	}

	if !reconciler.healthChecker.IsNetworkingHealthy() {
		_, err := reconciler.healthChecker.IsFRRActive()
		if err != nil {
			return fmt.Errorf("error checking FRR status: %w", err)
		}
		if err = reconciler.healthChecker.CheckInterfaces(); err != nil {
			return fmt.Errorf("error checking network interfaces: %w", err)
		}
		if err = reconciler.healthChecker.CheckReachability(); err != nil {
			return fmt.Errorf("error checking network reachability: %w", err)
		}
		if err = reconciler.healthChecker.RemoveTaints(ctx); err != nil {
			return fmt.Errorf("error removing taint from the node: %w", err)
		}
	}

	return nil
}
