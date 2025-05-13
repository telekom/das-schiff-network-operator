package operator

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
	defaultDebounceTime    = 1 * time.Second
	DefaultTimeout         = "60s"
	DefaultNodeUpdateLimit = 1
)

// ConfigReconciler is responsible for creating NetworkConfigRevision objects.
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

// // NewConfigReconciler creates new reconciler that creates NetworkConfigRevision objects.
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

	// Get HBRConfigs
	l2vnis, l3vnis, bgps, err := r.fetchConfigData(timeoutCtx)
	if err != nil {
		return fmt.Errorf("failed to fetch configuration details: %w", err)
	}

	// prepare new revision
	revision, err := v1alpha1.NewRevision(l2vnis, l3vnis, bgps)
	if err != nil {
		return fmt.Errorf("failed to prepare new NetworkConfigRevision: %w", err)
	}

	r.logger.Info("new NetworkConfigRevision prepared", "name", revision.Name)

	// get all known revisions
	revisions, err := listRevisions(timeoutCtx, cr.client)
	if err != nil {
		return fmt.Errorf("failed to list NetworkConfigRevisions: %w", err)
	}

	// check if revision should be skipped (e.g. it is the same as known invalid revision, or as currently deployed revision)
	if cr.shouldSkip(revisions, revision) {
		return nil
	}

	// create revision object
	if err := r.createRevision(timeoutCtx, revision); err != nil {
		return fmt.Errorf("faild to create NetworkConfigRevision %s: %w", revision.Name, err)
	}

	cr.logger.Info("deployed NetworkConfigRevision", "name", revision.Name)
	return nil
}

func (cr *ConfigReconciler) shouldSkip(revisions *v1alpha1.NetworkConfigRevisionList, processedRevision *v1alpha1.NetworkConfigRevision) bool {
	if len(revisions.Items) > 0 && revisions.Items[0].Spec.Revision == processedRevision.Spec.Revision {
		cr.logger.Info("NetworkConfigRevision creation aborted - new revision equals to the last known one")
		// new NetworkConfigRevision equals to the last known one - skip (no update is required)
		return true
	}

	for i := range revisions.Items {
		if !revisions.Items[i].Status.IsInvalid {
			if revisions.Items[i].Spec.Revision == processedRevision.Spec.Revision {
				cr.logger.Info("NetworkConfigRevision creation aborted - new revision equals to the last known valid one")
				// new NetworkConfigRevision equals to the last known valid one - skip (should be already deployed)
				return true
			}
			break
		}
	}

	for i := range revisions.Items {
		if (revisions.Items[i].Spec.Revision == processedRevision.Spec.Revision) && revisions.Items[i].Status.IsInvalid {
			// new NetworkConfigRevision is equal to known invalid revision - skip
			cr.logger.Info("NetworkConfigRevision creation aborted - new revision is equal to known invalid revision")
			return true
		}
	}

	return false
}

func (r *reconcileConfig) createRevision(ctx context.Context, revision *v1alpha1.NetworkConfigRevision) error {
	r.logger.Info("creating NetworkConfigRevision", "name", revision.Name)
	if err := r.client.Create(ctx, revision); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("error creating NodeConfigRevision: %w", err)
		}
		if err := r.client.Delete(ctx, revision); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("error deleting old instance of revision %s: %w", revision.Name, err)
		}
		if err := r.client.Create(ctx, revision); err != nil {
			return fmt.Errorf("error creating new instance of revision %s: %w", revision.Name, err)
		}
	}
	return nil
}

func (r *reconcileConfig) fetchLayer2(ctx context.Context) ([]v1alpha1.Layer2Revision, error) {
	layer2List := &v1alpha1.Layer2NetworkConfigurationList{}
	err := r.client.List(ctx, layer2List)
	if err != nil {
		r.Logger.Error(err, "error getting list of Layer2s from Kubernetes")
		return nil, fmt.Errorf("error getting list of Layer2s from Kubernetes: %w", err)
	}

	if err := checkL2Duplicates(layer2List.Items); err != nil {
		return nil, err
	}

	l2vnis := make([]v1alpha1.Layer2Revision, len(layer2List.Items))
	for i := range layer2List.Items {
		l2vnis[i] = v1alpha1.Layer2Revision{
			Name:                           layer2List.Items[i].Name,
			Layer2NetworkConfigurationSpec: layer2List.Items[i].Spec,
		}
	}

	return l2vnis, nil
}

func (r *reconcileConfig) fetchLayer3(ctx context.Context) ([]v1alpha1.VRFRevision, error) {
	vrfs := &v1alpha1.VRFRouteConfigurationList{}
	err := r.client.List(ctx, vrfs)
	if err != nil {
		r.Logger.Error(err, "error getting list of VRFs from Kubernetes")
		return nil, fmt.Errorf("error getting list of VRFs from Kubernetes: %w", err)
	}

	l3vnis := make([]v1alpha1.VRFRevision, len(vrfs.Items))
	for i := range vrfs.Items {
		l3vnis[i] = v1alpha1.VRFRevision{
			Name:                      vrfs.Items[i].Name,
			VRFRouteConfigurationSpec: vrfs.Items[i].Spec,
		}
	}

	return l3vnis, nil
}

func (r *reconcileConfig) fetchBgp(ctx context.Context) ([]v1alpha1.BGPRevision, error) {
	bgpConfigs := &v1alpha1.BGPPeeringList{}
	err := r.client.List(ctx, bgpConfigs)
	if err != nil {
		r.Logger.Error(err, "error getting list of BGP peering configurations from Kubernetes")
		return nil, fmt.Errorf("error getting list of BGP peering configurations from Kubernetes: %w", err)
	}

	bgps := make([]v1alpha1.BGPRevision, len(bgpConfigs.Items))
	for i := range bgpConfigs.Items {
		bgps[i] = v1alpha1.BGPRevision{
			Name:           bgpConfigs.Items[i].Name,
			BGPPeeringSpec: bgpConfigs.Items[i].Spec,
		}
	}

	return bgps, nil
}

func (r *reconcileConfig) fetchConfigData(ctx context.Context) ([]v1alpha1.Layer2Revision, []v1alpha1.VRFRevision, []v1alpha1.BGPRevision, error) {
	// get Layer2networkConfiguration objects
	l2vnis, err := r.fetchLayer2(ctx)
	if err != nil {
		r.Logger.Error(err, "error getting list of Layer2s from Kubernetes")
		return nil, nil, nil, fmt.Errorf("error getting list of Layer2s from Kubernetes: %w", err)
	}

	// get VRFRouteConfiguration objects
	l3vnis, err := r.fetchLayer3(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	// get BGPPeering objects
	bgps, err := r.fetchBgp(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	return l2vnis, l3vnis, bgps, nil
}

func listRevisions(ctx context.Context, c client.Client) (*v1alpha1.NetworkConfigRevisionList, error) {
	revisions := &v1alpha1.NetworkConfigRevisionList{}
	if err := c.List(ctx, revisions); err != nil {
		return nil, fmt.Errorf("error listing NetworkConfigRevisions: %w", err)
	}

	// sort revisions by creation date ascending (newest first)
	if len(revisions.Items) > 0 {
		slices.SortFunc(revisions.Items, func(a, b v1alpha1.NetworkConfigRevision) int {
			return b.GetCreationTimestamp().Compare(a.GetCreationTimestamp().Time) // newest first
		})
	}

	return revisions, nil
}
