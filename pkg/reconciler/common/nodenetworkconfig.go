/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package common provides shared functionality for node network config reconcilers.
package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
	"github.com/telekom/das-schiff-network-operator/pkg/routedcni"
)

const (
	// DefaultNodeNetworkConfigPath is the default path to store the current config.
	DefaultNodeNetworkConfigPath = "/opt/network-operator/current-config.yaml"
	// NodeNetworkConfigFilePerm is the file permission for the config file.
	NodeNetworkConfigFilePerm = 0o600
	// TaintRemovalRequeueTime is the delay before retrying taint removal after a conflict.
	TaintRemovalRequeueTime = 30 * time.Second
)

// ConfigApplier is an interface for applying network configuration.
// Each agent implements this interface with its own logic.
type ConfigApplier interface {
	// ApplyConfig applies the network configuration.
	ApplyConfig(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig) error
}

// RoutedPortsSource supplies the routed-port attachments recorded for this node
// (via the routed CNI, over the node-local gRPC channel) so they can be merged
// into the NodeNetworkConfig before it is rendered. Agents that do not use the
// routed CNI (e.g. FRR, which programs the CRA-side FIB directly) leave it nil.
type RoutedPortsSource interface {
	RoutedPorts(ctx context.Context) ([]v1alpha1.RoutedPortEntry, error)
}

// ReconcilerOptions contains configuration options for the reconciler.
type ReconcilerOptions struct {
	// RestoreOnReconcileFailure controls whether to restore the previous config
	// when reconciliation fails. Set to true for agents like FRR where invalid
	// configs can be partially applied. Set to false for agents like VSR where
	// invalid configs cannot be committed.
	RestoreOnReconcileFailure bool

	// LocalASN is the local (platform-side) BGP autonomous system number the
	// agent is configured with (base config localASN). It is surfaced onto the
	// node's NodeNetworkConfig.status.asNumber so the operator can report the
	// server ASN on BGPPeering status. Zero means unset (nothing is surfaced).
	LocalASN int

	// RoutedPortsSource, when set, supplies routed-port attachments recorded for
	// this node; they are merged into the NodeNetworkConfig before rendering.
	// Used by VSR, whose CRA-side FIB is programmed by the agent (NETCONF) rather
	// than by the CNI. Leave nil to disable.
	RoutedPortsSource RoutedPortsSource
}

// NodeNetworkConfigReconciler handles the common reconciliation logic for NodeNetworkConfig.
type NodeNetworkConfigReconciler struct {
	client                    client.Client
	logger                    logr.Logger
	healthChecker             healthcheck.HealthCheckerInterface
	configApplier             ConfigApplier
	NodeNetworkConfig         *v1alpha1.NodeNetworkConfig
	NodeNetworkConfigPath     string
	restoreOnReconcileFailure bool
	localASN                  int64
	routedPortsSource         RoutedPortsSource
	lastRoutedPortsHash       string
}

// NewNodeNetworkConfigReconciler creates a new NodeNetworkConfigReconciler.
func NewNodeNetworkConfigReconciler(
	clusterClient client.Client,
	logger logr.Logger,
	configApplier ConfigApplier,
	nodeNetworkConfigPath string,
	opts ReconcilerOptions,
) (*NodeNetworkConfigReconciler, error) {
	reconciler := &NodeNetworkConfigReconciler{
		client:                    clusterClient,
		logger:                    logger,
		configApplier:             configApplier,
		NodeNetworkConfigPath:     nodeNetworkConfigPath,
		restoreOnReconcileFailure: opts.RestoreOnReconcileFailure,
		localASN:                  int64(opts.LocalASN),
		routedPortsSource:         opts.RoutedPortsSource,
	}

	nc, err := healthcheck.LoadConfig(healthcheck.NetHealthcheckFile)
	if err != nil {
		return nil, fmt.Errorf("error loading networking healthcheck config: %w", err)
	}

	tcpDialer := healthcheck.NewTCPDialer(nc.Timeout)
	reconciler.healthChecker, err = healthcheck.NewHealthChecker(
		reconciler.client,
		healthcheck.NewDefaultHealthcheckToolkit(tcpDialer),
		nc)
	if err != nil {
		return nil, fmt.Errorf("error creating networking healthchecker: %w", err)
	}

	reconciler.NodeNetworkConfig, err = ReadNodeNetworkConfig(reconciler.NodeNetworkConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("error reading NodeNetworkConfig from disk: %w", err)
	}

	return reconciler, nil
}

// Reconcile performs the main reconciliation logic for NodeNetworkConfig.
// Returns a Result indicating whether a requeue is needed (e.g., for retrying taint removal).
func (r *NodeNetworkConfigReconciler) Reconcile(ctx context.Context) (ctrl.Result, error) {
	// get NodeNetworkConfig from apiserver
	cfg, err := r.fetchNodeConfig(ctx)
	if err != nil {
		// discard IsNotFound error
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Surface the agent's local (platform-side) ASN onto the node's status so
	// the operator can report the server ASN on BGPPeering status. This is done
	// in-band with the existing status writes below (no separate API call) so it
	// can never block config provisioning. asnNeedsWrite forces a status write in
	// the already-provisioned fast path when the stored ASN is out of date (e.g.
	// first run after upgrade, or a cleared value when localASN is unset).
	asnNeedsWrite := cfg.Status.ASNumber != r.localASN
	cfg.Status.ASNumber = r.localASN

	// Merge routed-port attachments recorded for this node (via the routed CNI)
	// into the fetched config before rendering. These arrive out-of-band from the
	// NodeNetworkConfig revision, so a change is tracked by a content hash that
	// forces re-rendering even when the revision is unchanged.
	routedHash, err := r.mergeRoutedPorts(ctx, cfg)
	if err != nil {
		return ctrl.Result{}, err
	}

	if r.NodeNetworkConfig != nil && r.NodeNetworkConfig.Spec.Revision == cfg.Spec.Revision &&
		r.lastRoutedPortsHash == routedHash {
		// replace in-memory working NodeNetworkConfig and store it on the disk
		if err := r.storeConfig(cfg, r.NodeNetworkConfigPath); err != nil {
			return ctrl.Result{}, fmt.Errorf("error saving NodeNetworkConfig status: %w", err)
		}
		r.lastRoutedPortsHash = routedHash

		// current in-memory config has the same revision as the fetched one
		// this means that NodeNetworkConfig was already provisioned - skip
		if cfg.Status.ConfigStatus != operator.StatusProvisioned || asnNeedsWrite {
			if err := SetStatus(ctx, r.client, cfg, operator.StatusProvisioned, r.logger); err != nil {
				return ctrl.Result{}, fmt.Errorf("error setting NodeNetworkConfig status: %w", err)
			}
		}

		// Attempt taint removal if not yet done (best-effort, don't fail on error)
		if !r.healthChecker.TaintsRemoved() {
			if err := r.healthChecker.RemoveTaints(ctx); err != nil {
				r.logger.Error(err, "failed to remove taints from node, will retry on next reconciliation")
				// Requeue after a short delay to retry taint removal
				return ctrl.Result{RequeueAfter: TaintRemovalRequeueTime}, nil
			}
		}

		return ctrl.Result{}, nil
	}

	// NodeNetworkConfig is invalid - discard
	if cfg.Spec.Revision == cfg.Status.LastAppliedRevision && cfg.Status.ConfigStatus == operator.StatusInvalid {
		r.logger.Info("skipping invalid NodeNetworkConfig", "name", cfg.Name)
		return ctrl.Result{}, nil
	}

	result, err := r.processConfig(ctx, cfg)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error while processing NodeNetworkConfig: %w", err)
	}

	// replace in-memory working NodeNetworkConfig and store it on the disk
	if err := r.storeConfig(cfg, r.NodeNetworkConfigPath); err != nil {
		return ctrl.Result{}, fmt.Errorf("error saving NodeNetworkConfig status: %w", err)
	}
	r.lastRoutedPortsHash = routedHash

	return result, nil
}

// mergeRoutedPorts merges routed-port attachments recorded for this node into
// cfg and returns a content hash of the merged entries. When no source is
// configured (e.g. FRR) it is a no-op returning an empty hash.
func (r *NodeNetworkConfigReconciler) mergeRoutedPorts(
	ctx context.Context,
	cfg *v1alpha1.NodeNetworkConfig,
) (string, error) {
	if r.routedPortsSource == nil {
		return "", nil
	}
	entries, err := r.routedPortsSource.RoutedPorts(ctx)
	if err != nil {
		return "", fmt.Errorf("error fetching routed ports: %w", err)
	}
	routedcni.MergeIntoNodeNetworkConfig(cfg, entries)
	return routedcni.HashEntries(entries), nil
}

func (r *NodeNetworkConfigReconciler) storeConfig(
	cfg *v1alpha1.NodeNetworkConfig,
	path string,
) error {
	r.NodeNetworkConfig = cfg

	// save working NodeNetworkConfig
	c, err := json.MarshalIndent(*r.NodeNetworkConfig, "", " ")
	if err != nil {
		panic(err)
	}

	if err = os.WriteFile(path, c, NodeNetworkConfigFilePerm); err != nil {
		return fmt.Errorf("error saving NodeNetworkConfig status: %w", err)
	}

	return nil
}

func (r *NodeNetworkConfigReconciler) fetchNodeConfig(
	ctx context.Context,
) (*v1alpha1.NodeNetworkConfig, error) {
	cfg := &v1alpha1.NodeNetworkConfig{}

	err := r.client.Get(ctx, types.NamespacedName{Name: os.Getenv(healthcheck.NodenameEnv)}, cfg)
	if err != nil {
		return nil, fmt.Errorf("error getting NodeConfig: %w", err)
	}

	return cfg, nil
}

func (r *NodeNetworkConfigReconciler) invalidateNodeNetworkConfig(
	ctx context.Context,
	cfg *v1alpha1.NodeNetworkConfig,
	reason string,
	errorMsg string,
) error {
	r.logger.Info("invalidating NodeNetworkConfig", "name", cfg.Name, "reason", reason, "error", errorMsg)

	if err := SetStatusWithError(ctx, r.client, cfg, operator.StatusInvalid, errorMsg, r.logger); err != nil {
		return fmt.Errorf("error invalidating NodeNetworkConfig: %w", err)
	}

	return nil
}

func (r *NodeNetworkConfigReconciler) invalidateAndRestore(
	ctx context.Context,
	cfg *v1alpha1.NodeNetworkConfig,
	reason string,
	errorMsg string,
) error {
	if err := r.invalidateNodeNetworkConfig(ctx, cfg, reason, errorMsg); err != nil {
		return err
	}

	// try to restore previously known good NodeNetworkConfig
	r.logger.Info("restoring previous NodeNetworkConfig")
	if err := r.restoreNodeNetworkConfig(ctx); err != nil {
		return fmt.Errorf("error restoring NodeNetworkConfig: %w", err)
	}

	return nil
}

func (r *NodeNetworkConfigReconciler) restoreNodeNetworkConfig(ctx context.Context) error {
	if r.NodeNetworkConfig == nil {
		return nil
	}

	if err := r.doReconciliation(ctx, r.NodeNetworkConfig); err != nil {
		return fmt.Errorf("error restoring NodeNetworkConfig: %w", err)
	}

	r.logger.Info("restored last known valid NodeNetworkConfig")

	return nil
}

func (r *NodeNetworkConfigReconciler) checkHealth(ctx context.Context) (ctrl.Result, error) {
	if err := r.healthChecker.CheckInterfaces(); err != nil {
		_ = r.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonInterfaceCheckFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("error checking network interfaces: %w", err)
	}

	if err := r.healthChecker.CheckReachability(); err != nil {
		_ = r.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonReachabilityFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("error checking network reachability: %w", err)
	}

	if err := r.healthChecker.CheckAPIServer(ctx); err != nil {
		_ = r.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonAPIServerFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("error checking API Server reachability: %w", err)
	}

	// All checks passed - update condition to healthy
	if err := r.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionTrue, healthcheck.ReasonHealthChecksPassed, "All network operator health checks passed"); err != nil {
		r.logger.Error(err, "failed to update network operator readiness condition")
	}

	// Taint removal is best-effort and should not fail the health check.
	// If it fails, request a requeue to retry taint removal.
	if !r.healthChecker.TaintsRemoved() {
		if err := r.healthChecker.RemoveTaints(ctx); err != nil {
			r.logger.Error(err, "failed to remove taints from node, will retry on next reconciliation")
			return ctrl.Result{RequeueAfter: TaintRemovalRequeueTime}, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *NodeNetworkConfigReconciler) processConfig(
	ctx context.Context,
	cfg *v1alpha1.NodeNetworkConfig,
) (ctrl.Result, error) {
	// set NodeNetworkConfig status as provisioning
	if err := SetStatus(ctx, r.client, cfg, operator.StatusProvisioning, r.logger); err != nil {
		return ctrl.Result{}, fmt.Errorf("error setting NodeNetworkConfig status %s: %w", operator.StatusProvisioning, err)
	}

	// reconcile NodeNetworkConfig
	if err := r.doReconciliation(ctx, cfg); err != nil {
		reconcileErrMsg := err.Error()
		// if reconciliation failed set NodeNetworkConfig's status as invalid
		if r.restoreOnReconcileFailure {
			// restore last known working NodeNetworkConfig (for agents like FRR where
			// invalid configs can be partially applied)
			if restoreErr := r.invalidateAndRestore(ctx, cfg, "reconciliation failed", reconcileErrMsg); restoreErr != nil {
				return ctrl.Result{}, fmt.Errorf("error restoring NodeNetworkConfig: %w", restoreErr)
			}
		} else {
			// no need to restore the config, the new one has not been applied
			// (for agents like VSR where invalid configs cannot be committed)
			if invalidateErr := r.invalidateNodeNetworkConfig(ctx, cfg, "reconciliation failed", reconcileErrMsg); invalidateErr != nil {
				return ctrl.Result{}, fmt.Errorf("error invalidating NodeNetworkConfig: %w", invalidateErr)
			}
		}

		return ctrl.Result{}, fmt.Errorf("reconciler error: %w", err)
	}

	// check if node is healthy after reconciliation
	result, err := r.checkHealth(ctx)
	if err != nil {
		healthErrMsg := err.Error()
		// if node is not healthy set NodeNetworkConfig's status as invalid
		// and restore last known working NodeNetworkConfig
		if restoreErr := r.invalidateAndRestore(ctx, cfg, "healthcheck failed", healthErrMsg); restoreErr != nil {
			return ctrl.Result{}, fmt.Errorf("failed to restore NodeNetworkConfig: %w", restoreErr)
		}

		return ctrl.Result{}, fmt.Errorf("healthcheck error (previous NodeNetworkConfig restored): %w", err)
	}

	// set NodeNetworkConfig status as provisioned (valid)
	if err := SetStatus(ctx, r.client, cfg, operator.StatusProvisioned, r.logger); err != nil {
		return ctrl.Result{}, fmt.Errorf("error setting NodeNetworkConfig status %s: %w", operator.StatusProvisioned, err)
	}

	return result, nil
}

func (r *NodeNetworkConfigReconciler) doReconciliation(
	ctx context.Context,
	nodeCfg *v1alpha1.NodeNetworkConfig,
) error {
	r.logger.Info("config to reconcile", "NodeNetworkConfig", *nodeCfg)

	if err := r.configApplier.ApplyConfig(ctx, nodeCfg); err != nil {
		return fmt.Errorf("error applying configuration: %w", err)
	}

	return nil
}

// ReadNodeNetworkConfig reads and parses a NodeNetworkConfig from a file.
func ReadNodeNetworkConfig(path string) (*v1alpha1.NodeNetworkConfig, error) {
	cfg, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading NodeNetworkConfig: %w", err)
	}

	nodeNetworkConfig := &v1alpha1.NodeNetworkConfig{}
	if err := json.Unmarshal(cfg, nodeNetworkConfig); err != nil {
		return nil, fmt.Errorf("error unmarshalling NodeNetworkConfig: %w", err)
	}

	return nodeNetworkConfig, nil
}

// SetStatus updates the status of a NodeNetworkConfig.
func SetStatus(
	ctx context.Context,
	c client.Client,
	cfg *v1alpha1.NodeNetworkConfig,
	status string,
	logger logr.Logger,
) error {
	return SetStatusWithError(ctx, c, cfg, status, "", logger)
}

// SetStatusWithError updates the status of a NodeNetworkConfig with an optional error message.
func SetStatusWithError(
	ctx context.Context,
	c client.Client,
	cfg *v1alpha1.NodeNetworkConfig,
	status string,
	errorMsg string,
	logger logr.Logger,
) error {
	logger.Info("setting NodeNetworkConfig status", "name", cfg.Name, "status", status)

	// Write the status subresource against a copy: client.Status().Update refreshes
	// the passed object from the server, which would otherwise revert any in-memory
	// additions to cfg.Spec (e.g. routed ports merged from NodeRoutedPorts) back to
	// the persisted, unmerged spec before the config is rendered/applied.
	statusObj := cfg.DeepCopy()
	statusObj.Status.ConfigStatus = status
	statusObj.Status.LastUpdate = metav1.Now()

	if status == operator.StatusProvisioned || status == operator.StatusInvalid {
		statusObj.Status.LastAppliedRevision = statusObj.Spec.Revision
	}

	// Set or clear error message based on status
	if status == operator.StatusInvalid {
		statusObj.Status.ErrorMessage = errorMsg
	} else {
		statusObj.Status.ErrorMessage = ""
	}

	if err := c.Status().Update(ctx, statusObj); err != nil {
		return fmt.Errorf("error updating NodeNetworkConfig status: %w", err)
	}

	// Propagate the persisted status and resource version back onto cfg without
	// clobbering cfg.Spec (which may carry merged routed ports).
	cfg.Status = statusObj.Status
	cfg.ResourceVersion = statusObj.ResourceVersion

	return nil
}

// GetNodeNetworkConfig returns the current in-memory NodeNetworkConfig.
func (r *NodeNetworkConfigReconciler) GetNodeNetworkConfig() *v1alpha1.NodeNetworkConfig {
	return r.NodeNetworkConfig
}
