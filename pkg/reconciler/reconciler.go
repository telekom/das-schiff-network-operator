package reconciler

import (
	"context"
	"os"
	"time"

	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Reconciler struct {
	client         client.Client
	netlinkManager *nl.NetlinkManager
	frrManager     *frr.FRRManager
	anycastTracker *anycast.AnycastTracker
	config         *config.Config

	debouncer *debounce.Debouncer

	dirtyFRRConfig bool
}

type reconcile struct {
	*Reconciler
	context.Context
	logr.Logger
}

func NewReconciler(client client.Client, anycastTracker *anycast.AnycastTracker) (*Reconciler, error) {
	reconciler := &Reconciler{
		client:         client,
		netlinkManager: &nl.NetlinkManager{},
		frrManager:     frr.NewFRRManager(),
		anycastTracker: anycastTracker,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.reconcileDebounced, 20*time.Second)

	if val := os.Getenv("FRR_CONFIG_FILE"); val != "" {
		reconciler.frrManager.ConfigPath = val
	}
	if err := reconciler.frrManager.Init(); err != nil {
		return nil, err
	}

	if config, err := config.LoadConfig(); err != nil {
		return nil, err
	} else {
		reconciler.config = config
	}

	return reconciler, nil
}

func (reconciler *Reconciler) Reconcile(ctx context.Context) {
	reconciler.debouncer.Debounce(ctx)
}

func (reconciler *Reconciler) reconcileDebounced(ctx context.Context) error {
	r := &reconcile{
		Reconciler: reconciler,
		Context:    ctx,
		Logger:     log.FromContext(ctx),
	}

	l3vnis, err := r.fetchLayer3()
	if err != nil {
		return err
	}
	l2vnis, err := r.fetchLayer2()
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
