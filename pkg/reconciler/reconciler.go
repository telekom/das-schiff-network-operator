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

	var cfg *config.Config
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	reconciler.config = cfg

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
	return nil
}
