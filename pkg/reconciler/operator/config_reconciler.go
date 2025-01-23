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
	l2vnis, l3vnis, err := r.fetchConfigData(timeoutCtx)
	if err != nil {
		return fmt.Errorf("failed to fetch configuration details: %w", err)
	}

	// prepare new revision
	revision, err := v1alpha1.NewRevision(l2vnis, l3vnis)
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

func checkL2Duplicates(configs []v1alpha1.Layer2NetworkConfiguration) error {
	for i := range configs {
		for j := i + 1; j < len(configs); j++ {
			if configs[i].Spec.ID == configs[j].Spec.ID {
				return fmt.Errorf("dupliate Layer2 ID found: %s %s", configs[i].ObjectMeta.Name, configs[j].ObjectMeta.Name)
			}
			if configs[i].Spec.VNI == configs[j].Spec.VNI {
				return fmt.Errorf("dupliate Layer2 VNI found: %s %s", configs[i].ObjectMeta.Name, configs[j].ObjectMeta.Name)
			}
		}
	}
	return nil
}

func (r *reconcileConfig) fetchLayer2(ctx context.Context) ([]v1alpha1.Layer2NetworkConfigurationSpec, error) {
	layer2List := &v1alpha1.Layer2NetworkConfigurationList{}
	err := r.client.List(ctx, layer2List)
	if err != nil {
		r.Logger.Error(err, "error getting list of Layer2s from Kubernetes")
		return nil, fmt.Errorf("error getting list of Layer2s from Kubernetes: %w", err)
	}

	if err := checkL2Duplicates(layer2List.Items); err != nil {
		return nil, err
	}

	var l2vnis []v1alpha1.Layer2NetworkConfigurationSpec
	for _, l2 := range layer2List.Items {
		l2vnis = append(l2vnis, l2.Spec)
	}

	return l2vnis, nil
}

func (r *reconcileConfig) fetchLayer3(ctx context.Context) ([]v1alpha1.VRFRouteConfigurationSpec, error) {
	vrfs := &v1alpha1.VRFRouteConfigurationList{}
	err := r.client.List(ctx, vrfs)
	if err != nil {
		r.Logger.Error(err, "error getting list of VRFs from Kubernetes")
		return nil, fmt.Errorf("error getting list of VRFs from Kubernetes: %w", err)
	}

	var l3vnis []v1alpha1.VRFRouteConfigurationSpec
	for _, l3 := range vrfs.Items {
		l3vnis = append(l3vnis, l3.Spec)
	}

	return l3vnis, nil
}

func (r *reconcileConfig) fetchConfigData(ctx context.Context) ([]v1alpha1.Layer2NetworkConfigurationSpec, []v1alpha1.VRFRouteConfigurationSpec, error) {
	// get Layer2networkConfiguration objects
	l2vnis, err := r.fetchLayer2(ctx)
	if err != nil {
		r.Logger.Error(err, "error getting list of Layer2s from Kubernetes")
		return nil, nil, fmt.Errorf("error getting list of Layer2s from Kubernetes: %w", err)
	}

	// get VRFRouteConfiguration objects
	l3vnis, err := r.fetchLayer3(ctx)
	if err != nil {
		return nil, nil, err
	}

	return l2vnis, l3vnis, nil
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
