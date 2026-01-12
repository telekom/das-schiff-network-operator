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

package agent_cra_vsr //nolint:revive

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	cra "github.com/telekom/das-schiff-network-operator/pkg/cra-vsr"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/common"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CRAVSRConfigApplier implements the common.ConfigApplier interface for CRA-VSR.
type CRAVSRConfigApplier struct {
	craManager *cra.Manager
}

// ApplyConfig applies the network configuration using CRA-VSR manager.
func (a *CRAVSRConfigApplier) ApplyConfig(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig) error {
	if err := a.craManager.ApplyConfiguration(ctx, &cfg.Spec); err != nil {
		return fmt.Errorf("error applying cra configuration: %w", err)
	}
	return nil
}

// NodeNetworkConfigReconciler wraps the common reconciler with CRA-VSR specific logic.
type NodeNetworkConfigReconciler struct {
	*common.NodeNetworkConfigReconciler
}

// NewNodeNetworkConfigReconciler creates a new NodeNetworkConfigReconciler for CRA-VSR.
func NewNodeNetworkConfigReconciler(
	craManager *cra.Manager,
	clusterClient client.Client,
	logger logr.Logger,
	nodeNetworkConfigPath string,
) (*NodeNetworkConfigReconciler, error) {
	configApplier := &CRAVSRConfigApplier{
		craManager: craManager,
	}

	commonReconciler, err := common.NewNodeNetworkConfigReconciler(
		clusterClient,
		logger,
		configApplier,
		nodeNetworkConfigPath,
		common.ReconcilerOptions{
			RestoreOnReconcileFailure: false, // VSR cannot commit invalid configs
		},
	)
	if err != nil {
		return nil, fmt.Errorf("error creating common reconciler: %w", err)
	}

	return &NodeNetworkConfigReconciler{
		NodeNetworkConfigReconciler: commonReconciler,
	}, nil
}
