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

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DefaultNodeNetworkConfigPath is the default path to store the current config.
	DefaultNodeNetworkConfigPath = "/opt/network-operator/current-config.yaml"
	// NodeNetworkConfigFilePerm is the file permission for the config file.
	NodeNetworkConfigFilePerm = 0o600
)

// ConfigApplier is an interface for applying network configuration.
// Each agent implements this interface with its own logic.
type ConfigApplier interface {
	// ApplyConfig applies the network configuration.
	ApplyConfig(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig) error
}

// ReconcilerOptions contains configuration options for the reconciler.
type ReconcilerOptions struct {
	// RestoreOnReconcileFailure controls whether to restore the previous config
	// when reconciliation fails. Set to true for agents like FRR where invalid
	// configs can be partially applied. Set to false for agents like VSR where
	// invalid configs cannot be committed.
	RestoreOnReconcileFailure bool
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
func (r *NodeNetworkConfigReconciler) Reconcile(ctx context.Context) error {
	// get NodeNetworkConfig from apiserver
	cfg, err := r.fetchNodeConfig(ctx)
	if err != nil {
		// discard IsNotFound error
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if r.NodeNetworkConfig != nil && r.NodeNetworkConfig.Spec.Revision == cfg.Spec.Revision {
		// replace in-memory working NodeNetworkConfig and store it on the disk
		if err := r.storeConfig(cfg, r.NodeNetworkConfigPath); err != nil {
			return fmt.Errorf("error saving NodeNetworkConfig status: %w", err)
		}

		// current in-memory config has the same revision as the fetched one
		// this means that NodeNetworkConfig was already provisioned - skip
		if cfg.Status.ConfigStatus != operator.StatusProvisioned {
			if err := SetStatus(ctx, r.client, cfg, operator.StatusProvisioned, r.logger); err != nil {
				return fmt.Errorf("error setting NodeNetworkConfig status: %w", err)
			}
		}
		return nil
	}

	// NodeNetworkConfig is invalid - discard
	if cfg.Spec.Revision == cfg.Status.LastAppliedRevision && cfg.Status.ConfigStatus == operator.StatusInvalid {
		r.logger.Info("skipping invalid NodeNetworkConfig", "name", cfg.Name)
		return nil
	}

	if err := r.processConfig(ctx, cfg); err != nil {
		return fmt.Errorf("error while processing NodeNetworkConfig: %w", err)
	}

	// replace in-memory working NodeNetworkConfig and store it on the disk
	if err := r.storeConfig(cfg, r.NodeNetworkConfigPath); err != nil {
		return fmt.Errorf("error saving NodeNetworkConfig status: %w", err)
	}

	return nil
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
) error {
	r.logger.Info("invalidating NodeNetworkConfig", "name", cfg.Name, "reason", reason)

	if err := SetStatus(ctx, r.client, cfg, operator.StatusInvalid, r.logger); err != nil {
		return fmt.Errorf("error invalidating NodeNetworkConfig: %w", err)
	}

	return nil
}

func (r *NodeNetworkConfigReconciler) invalidateAndRestore(
	ctx context.Context,
	cfg *v1alpha1.NodeNetworkConfig,
	reason string,
) error {
	if err := r.invalidateNodeNetworkConfig(ctx, cfg, reason); err != nil {
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

func (r *NodeNetworkConfigReconciler) checkHealth(ctx context.Context) error {
	if err := r.healthChecker.CheckInterfaces(); err != nil {
		return fmt.Errorf("error checking network interfaces: %w", err)
	}

	if err := r.healthChecker.CheckReachability(); err != nil {
		return fmt.Errorf("error checking network reachability: %w", err)
	}

	if err := r.healthChecker.CheckAPIServer(ctx); err != nil {
		return fmt.Errorf("error checking API Server reachability: %w", err)
	}

	if !r.healthChecker.TaintsRemoved() {
		if err := r.healthChecker.RemoveTaints(ctx); err != nil {
			return fmt.Errorf("error removing taint from the node: %w", err)
		}
	}

	return nil
}

func (r *NodeNetworkConfigReconciler) processConfig(
	ctx context.Context,
	cfg *v1alpha1.NodeNetworkConfig,
) error {
	// set NodeNetworkConfig status as provisioning
	if err := SetStatus(ctx, r.client, cfg, operator.StatusProvisioning, r.logger); err != nil {
		return fmt.Errorf("error setting NodeNetworkConfig status %s: %w", operator.StatusProvisioning, err)
	}

	// reconcile NodeNetworkConfig
	if err := r.doReconciliation(ctx, cfg); err != nil {
		// if reconciliation failed set NodeNetworkConfig's status as invalid
		if r.restoreOnReconcileFailure {
			// restore last known working NodeNetworkConfig (for agents like FRR where
			// invalid configs can be partially applied)
			if restoreErr := r.invalidateAndRestore(ctx, cfg, "reconciliation failed"); restoreErr != nil {
				return fmt.Errorf("error restoring NodeNetworkConfig: %w", restoreErr)
			}
		} else {
			// no need to restore the config, the new one has not been applied
			// (for agents like VSR where invalid configs cannot be committed)
			if invalidateErr := r.invalidateNodeNetworkConfig(ctx, cfg, "reconciliation failed"); invalidateErr != nil {
				return fmt.Errorf("error invalidating NodeNetworkConfig: %w", invalidateErr)
			}
		}

		return fmt.Errorf("reconciler error: %w", err)
	}

	// check if node is healthy after reconciliation
	if err := r.checkHealth(ctx); err != nil {
		// if node is not healthy set NodeNetworkConfig's status as invalid
		// and restore last known working NodeNetworkConfig
		if err := r.invalidateAndRestore(ctx, cfg, "healthcheck failed"); err != nil {
			return fmt.Errorf("failed to restore NodeNetworkConfig: %w", err)
		}

		return fmt.Errorf("healthcheck error (previous NodeNetworkConfig restored): %w", err)
	}

	// set NodeNetworkConfig status as provisioned (valid)
	if err := SetStatus(ctx, r.client, cfg, operator.StatusProvisioned, r.logger); err != nil {
		return fmt.Errorf("error setting NodeNetworkConfig status %s: %w", operator.StatusProvisioned, err)
	}

	return nil
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
	logger.Info("setting NodeNetworkConfig status", "name", cfg.Name, "status", status)

	cfg.Status.ConfigStatus = status
	cfg.Status.LastUpdate = metav1.Now()

	if status == operator.StatusProvisioned || status == operator.StatusInvalid {
		cfg.Status.LastAppliedRevision = cfg.Spec.Revision
	}

	if err := c.Status().Update(ctx, cfg); err != nil {
		return fmt.Errorf("error updating NodeNetworkConfig status: %w", err)
	}

	return nil
}

// GetNodeNetworkConfig returns the current in-memory NodeNetworkConfig.
func (r *NodeNetworkConfigReconciler) GetNodeNetworkConfig() *v1alpha1.NodeNetworkConfig {
	return r.NodeNetworkConfig
}
