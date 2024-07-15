package reconciler

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DefaultTimeout         = "60s"
	DefaultNodeUpdateLimit = 1
)

// ConfigReconciler is responsible for creating NodeConfig objects.
type ConfigReconciler struct {
	logger    logr.Logger
	debouncer *debounce.Debouncer
	client    client.Client
	timeout   time.Duration
}

type reconcileConfig struct {
	*ConfigReconciler
	logr.Logger
}

// Reconcile starts reconciliation.
func (cr *ConfigReconciler) Reconcile(ctx context.Context) {
	cr.debouncer.Debounce(ctx)
}

// // NewConfigReconciler creates new reconciler that creates NodeConfig objects.
func NewConfigReconciler(clusterClient client.Client, logger logr.Logger, timeout time.Duration) (*ConfigReconciler, error) {
	reconciler := &ConfigReconciler{
		logger:  logger,
		timeout: timeout,
		client:  clusterClient,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.ReconcileDebounced, defaultDebounceTime, logger)

	return reconciler, nil
}

func (cr *ConfigReconciler) ReconcileDebounced(ctx context.Context) error {
	r := &reconcileConfig{
		ConfigReconciler: cr,
		Logger:           cr.logger,
	}

	cr.logger.Info("fetching config data...")

	timeoutCtx, cancel := context.WithTimeout(ctx, cr.timeout)
	defer cancel()

	// get VRFRouteConfiguration, Layer2networkConfiguration and RoutingTable objects
	configData, err := r.fetchConfigData(timeoutCtx)
	if err != nil {
		return fmt.Errorf("error fetching configuration details: %w", err)
	}

	// prepare new revision
	revision, err := v1alpha1.NewRevision(configData)
	if err != nil {
		return fmt.Errorf("error preparing new config revision: %w", err)
	}

	cr.logger.Info("new revision", "data", revision)

	// get all known revisions
	revisions, err := listRevisions(timeoutCtx, cr.client)
	if err != nil {
		return fmt.Errorf("error listing revisions: %w", err)
	}

	// check if revision should be skipped (e.g. it is the same as known invalid revision, or as currently deployed revision)
	if shouldSkip(revisions, revision) {
		return nil
	}

	// create revision object
	if err := r.createRevision(timeoutCtx, revision); err != nil {
		return fmt.Errorf("error creating revision %s: %w", revision.Spec.Revision, err)
	}

	cr.logger.Info("deployed", "revision", revision.Spec.Revision)
	return nil
}

func shouldSkip(revisions *v1alpha1.NetworkConfigRevisionList, processedRevision *v1alpha1.NetworkConfigRevision) bool {
	if len(revisions.Items) > 0 && processedRevision.Spec.Revision == revisions.Items[0].Spec.Revision {
		// new revision equals to the last known one - skip
		return true
	}

	for i := range revisions.Items {
		if (processedRevision.Spec.Revision == revisions.Items[i].Spec.Revision) && revisions.Items[i].Status.IsInvalid {
			// new revision is equal to known invalid revision
			return true
		}
	}

	return false
}

func (r *reconcileConfig) createRevision(ctx context.Context, revision *v1alpha1.NetworkConfigRevision) error {
	if err := r.client.Create(ctx, revision); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("error creating NodeConfigRevision: %w", err)
		}
		if err := r.client.Delete(ctx, revision); err != nil {
			return fmt.Errorf("error creating deleting already existing NodeConfigRevision: %w", err)
		}
		if err := r.client.Create(ctx, revision); err != nil {
			return fmt.Errorf("error creating NodeConfigRevision: %w", err)
		}
	}
	return nil
}

func (r *reconcileConfig) fetchConfigData(ctx context.Context) (*v1alpha1.NodeNetworkConfig, error) {
	// get VRFRouteConfiguration objects
	l3vnis, err := r.fetchLayer3(ctx)
	if err != nil {
		return nil, err
	}

	// get Layer2networkConfiguration objects
	l2vnis, err := r.fetchLayer2(ctx)
	if err != nil {
		return nil, err
	}

	// get RoutingTable objects
	taas, err := r.fetchTaas(ctx)
	if err != nil {
		return nil, err
	}

	config := &v1alpha1.NodeNetworkConfig{}

	// discard metadata from previously fetched objects
	config.Spec.Layer2 = []v1alpha1.Layer2NetworkConfigurationSpec{}
	for i := range l2vnis {
		config.Spec.Layer2 = append(config.Spec.Layer2, l2vnis[i].Spec)
	}

	config.Spec.Vrf = []v1alpha1.VRFRouteConfigurationSpec{}
	for i := range l3vnis {
		config.Spec.Vrf = append(config.Spec.Vrf, l3vnis[i].Spec)
	}

	config.Spec.RoutingTable = []v1alpha1.RoutingTableSpec{}
	for i := range taas {
		config.Spec.RoutingTable = append(config.Spec.RoutingTable, taas[i].Spec)
	}

	return config, nil
}

func listRevisions(ctx context.Context, c client.Client) (*v1alpha1.NetworkConfigRevisionList, error) {
	revisions := &v1alpha1.NetworkConfigRevisionList{}
	if err := c.List(ctx, revisions); err != nil {
		return nil, fmt.Errorf("error listing revisions: %w", err)
	}

	// sort revisions by creation date ascending (newest first)
	if len(revisions.Items) > 0 {
		slices.SortFunc(revisions.Items, func(a, b v1alpha1.NetworkConfigRevision) int {
			return b.ObjectMeta.CreationTimestamp.Compare(a.ObjectMeta.CreationTimestamp.Time) // newest first
		})
	}

	return revisions, nil
}
