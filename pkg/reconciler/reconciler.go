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
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const defaultDebounceTime = 20 * time.Second

type Reconciler struct {
	client         client.Client
	netlinkManager *nl.NetlinkManager
	frrManager     *frr.Manager
	anycastTracker *anycast.Tracker
	config         *config.Config
	healthChecker  *healthcheck.HealthChecker

	debouncer *debounce.Debouncer

	dirtyFRRConfig bool
}

type reconcile struct {
	*Reconciler
	logr.Logger
}

func NewReconciler(clusterClient client.Client, anycastTracker *anycast.Tracker) (*Reconciler, error) {
	reconciler := &Reconciler{
		client:         clusterClient,
		netlinkManager: &nl.NetlinkManager{},
		frrManager:     frr.NewFRRManager(),
		anycastTracker: anycastTracker,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.reconcileDebounced, defaultDebounceTime)

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
		Logger:     log.FromContext(ctx),
	}

	r.Logger.Info("Reloading config")
	if err := r.config.ReloadConfig(); err != nil {
		return err
	}

	l3vnis, err := r.fetchLayer3(ctx)
	if err != nil {
		return err
	}
	l2vnis, err := r.fetchLayer2(ctx)
	if err != nil {
		return err
	}

	if err := r.reconcileLayer3(l3vnis); err != nil {
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
		if err = reconciler.healthChecker.RemoveTaint(ctx, healthcheck.TaintKey); err != nil {
			return fmt.Errorf("error removing taint from the node: %w", err)
		}
	}

	return nil
}
