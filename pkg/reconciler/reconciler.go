package reconciler

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultDebounceTime = 20 * time.Second

type Adapter interface {
	reconcileLayer3([]networkv1alpha1.VRFRouteConfiguration, []networkv1alpha1.RoutingTable) error
	reconcileLayer2([]networkv1alpha1.Layer2NetworkConfiguration) error
	checkHealth(context.Context) error
	getConfig() *config.Config
}

type Reconciler struct {
	client         client.Client
	netlinkManager *nl.Manager
	frrManager     *frr.Manager
	anycastTracker *anycast.Tracker
	config         *config.Config
	logger         logr.Logger
	adapter        Adapter
	healthChecker  *healthcheck.HealthChecker

	debouncer *debounce.Debouncer
}

type reconcile struct {
	*Reconciler
	logr.Logger
}

func NewReconciler(clusterClient client.Client, anycastTracker *anycast.Tracker, logger logr.Logger, adapter Adapter) (*Reconciler, error) {
	reconciler := &Reconciler{
		client:         clusterClient,
		netlinkManager: nl.NewManager(&nl.Toolkit{}),
		frrManager:     frr.NewFRRManager(),
		anycastTracker: anycastTracker,
		logger:         logger,
		adapter:        adapter,
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

func (r *Reconciler) Reconcile(ctx context.Context) {
	r.debouncer.Debounce(ctx)
}

func (r *Reconciler) reconcileDebounced(ctx context.Context) error {
	reconciler := &reconcile{
		Reconciler: r,
		Logger:     r.logger,
	}

	reconciler.Logger.Info("Reloading config")
	if err := reconciler.adapter.getConfig().ReloadConfig(); err != nil {
		return fmt.Errorf("error reloading network-operator config: %w", err)
	}

	l3vnis, err := reconciler.fetchLayer3(ctx)
	if err != nil {
		return err
	}
	l2vnis, err := reconciler.fetchLayer2(ctx)
	if err != nil {
		return err
	}
	taas, err := reconciler.fetchTaas(ctx)
	if err != nil {
		return err
	}

	if err := reconciler.adapter.reconcileLayer3(l3vnis, taas); err != nil {
		return fmt.Errorf("error while configuring Layer3: %w", err)
	}
	if err := reconciler.adapter.reconcileLayer2(l2vnis); err != nil {
		return fmt.Errorf("error while configuring Layer2: %w", err)
	}

	if err := reconciler.adapter.checkHealth(ctx); err != nil {
		return fmt.Errorf("healthcheck error: %w", err)
	}

	return nil
}
