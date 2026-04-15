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
// This file contains exported helpers that are intended for use by test code in
// dependent packages (e.g. agent-cra-vsr, agent-cra-frr). They are in a non-test
// file solely so they can be imported from other packages' _test.go files; they
// must not be called from production (non-test) code paths.
package common

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewNodeNetworkConfigReconcilerForTesting creates a NodeNetworkConfigReconciler
// with a caller-supplied HealthCheckerInterface. This bypasses the real
// healthcheck initialisation (LoadConfig, TCP dialer) and is intended for
// unit tests in dependent packages that need to inject a stub or mock
// healthchecker.
//
// Do NOT call this function from production (non-test) code.
func NewNodeNetworkConfigReconcilerForTesting(
	clusterClient client.Client,
	logger logr.Logger,
	configApplier ConfigApplier,
	nodeNetworkConfigPath string,
	opts ReconcilerOptions,
	hc healthcheck.HealthCheckerInterface,
) (*NodeNetworkConfigReconciler, error) {
	reconciler := &NodeNetworkConfigReconciler{
		client:                    clusterClient,
		logger:                    logger,
		configApplier:             configApplier,
		healthChecker:             hc,
		NodeNetworkConfigPath:     nodeNetworkConfigPath,
		restoreOnReconcileFailure: opts.RestoreOnReconcileFailure,
	}

	var err error
	reconciler.NodeNetworkConfig, err = ReadNodeNetworkConfig(reconciler.NodeNetworkConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("error reading NodeNetworkConfig from disk: %w", err)
	}

	return reconciler, nil
}

// RestoreOnReconcileFailure returns whether the reconciler restores the previous
// configuration when reconciliation fails. Exposed for test assertion use only.
func (r *NodeNetworkConfigReconciler) RestoreOnReconcileFailure() bool {
	return r.restoreOnReconcileFailure
}
