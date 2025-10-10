package reconciler

import (
	"context"
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
	"github.com/telekom/das-schiff-network-operator/pkg/neighborsync"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/nltoolkit"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultDebounceTime = 20 * time.Second

type Reconciler struct {
	client         client.Client
	netlinkManager *nl.Manager
	frrManager     *frr.Manager
	anycastTracker *anycast.Tracker
	neighborSync   *neighborsync.NeighborSync
	config         *config.Config
	logger         logr.Logger
	healthChecker  *healthcheck.HealthChecker

	debouncer *debounce.Debouncer
}

type reconcile struct {
	*Reconciler
	logr.Logger
}

type reconcileData struct {
	l3vnis   []v1alpha1.VRFRouteConfiguration
	l2vnis   []v1alpha1.Layer2NetworkConfiguration
	taas     []v1alpha1.RoutingTable
	peerings []v1alpha1.BGPPeering
}

func NewReconciler(clusterClient client.Client, anycastTracker *anycast.Tracker, neighborSync *neighborsync.NeighborSync, logger logr.Logger) (*Reconciler, error) {
	reconciler := &Reconciler{
		client:         clusterClient,
		netlinkManager: nl.NewManager(&nltoolkit.Toolkit{}),
		frrManager:     frr.NewFRRManager(),
		anycastTracker: anycastTracker,
		neighborSync:   neighborSync,
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
	r := &reconcile{Reconciler: reconciler, Logger: reconciler.logger}
	if err := r.reloadConfig(); err != nil {
		return err
	}
	data, err := r.collectData(ctx)
	if err != nil {
		return err
	}
	if err := r.reconcileDataSets(data); err != nil {
		return err
	}
	if err := r.ensureNodeReady(ctx); err != nil {
		return err
	}
	return nil
}

func (r *reconcile) reloadConfig() error {
	r.Info("Reloading config")
	if err := r.config.ReloadConfig(); err != nil {
		return fmt.Errorf("error reloading network-operator config: %w", err)
	}
	return nil
}

func (r *reconcile) collectData(ctx context.Context) (*reconcileData, error) {
	l3vnis, err := r.fetchLayer3(ctx)
	if err != nil {
		return nil, err
	}
	l2vnis, err := r.fetchLayer2(ctx)
	if err != nil {
		return nil, err
	}
	taas, err := r.fetchTaas(ctx)
	if err != nil {
		return nil, err
	}
	peerings, err := r.fetchBGPPeerings(ctx)
	if err != nil {
		return nil, err
	}
	return &reconcileData{l3vnis: l3vnis, l2vnis: l2vnis, taas: taas, peerings: peerings}, nil
}

func (r *reconcile) reconcileDataSets(data *reconcileData) error {
	if err := r.reconcileLayer3(data); err != nil {
		return err
	}
	if err := r.reconcileLayer2(data); err != nil {
		return err
	}
	return nil
}

func (r *reconcile) ensureNodeReady(ctx context.Context) error {
	if r.healthChecker.TaintsRemoved() {
		return nil
	}
	if _, err := r.healthChecker.IsFRRActive(); err != nil {
		_ = r.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonReachabilityFailed, fmt.Sprintf("FRR inactive: %v", err))
		return fmt.Errorf("error checking FRR status: %w", err)
	}
	if err := r.healthChecker.CheckInterfaces(); err != nil {
		_ = r.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonInterfaceCheckFailed, err.Error())
		return fmt.Errorf("error checking network interfaces: %w", err)
	}
	if err := r.healthChecker.CheckReachability(); err != nil {
		_ = r.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonReachabilityFailed, err.Error())
		return fmt.Errorf("error checking network reachability: %w", err)
	}
	if err := r.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionTrue, healthcheck.ReasonHealthChecksPassed, "All network operator health checks passed"); err != nil {
		r.logger.Error(err, "failed to update network operator readiness condition")
	}
	if err := r.healthChecker.RemoveTaints(ctx); err != nil {
		return fmt.Errorf("error removing taint from the node: %w", err)
	}
	return nil
}
