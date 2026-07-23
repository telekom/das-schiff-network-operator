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

package agent_cra_grout //nolint:revive

import (
	"context"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	crafrr "github.com/telekom/das-schiff-network-operator/pkg/cra-frr"
	cra "github.com/telekom/das-schiff-network-operator/pkg/cra-grout"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/common"
	"github.com/telekom/das-schiff-network-operator/pkg/routedcni"
)

const (
	baseConfigPath  = "/etc/cra/config/base-config.yaml"
	frrTemplatePath = "/opt/network-operator/frr.conf.tpl"
)

// CRAGroutConfigApplier implements the common.ConfigApplier interface for the
// cra-grout flavor: it renders the FRR control-plane config (reusing the cra-frr
// template) and the grout fast-path grcli batch, then applies both via the
// cra-grout manager (POSTed to the grout-cra sidecar).
type CRAGroutConfigApplier struct {
	craManager  *cra.Manager
	baseConfig  *config.BaseConfig
	frrTemplate crafrr.FRRTemplate
}

// ApplyConfig renders and applies the FRR config + grcli batch for the node.
func (a *CRAGroutConfigApplier) ApplyConfig(ctx context.Context, cfg *v1alpha1.NodeNetworkConfig) error {
	frrConfig, err := a.frrTemplate.TemplateFRR(a.baseConfig, &cfg.Spec)
	if err != nil {
		return fmt.Errorf("error templating FRR configuration: %w", err)
	}

	grcliBatch, err := cra.RenderGrcli(a.baseConfig, &cfg.Spec)
	if err != nil {
		return fmt.Errorf("error rendering grcli batch: %w", err)
	}

	if err := a.craManager.ApplyConfiguration(ctx, frrConfig, grcliBatch); err != nil {
		return fmt.Errorf("error applying cra configuration: %w", err)
	}

	return nil
}

// NodeNetworkConfigReconciler wraps the common reconciler with cra-grout logic.
type NodeNetworkConfigReconciler struct {
	*common.NodeNetworkConfigReconciler
}

// NewNodeNetworkConfigReconciler creates a NodeNetworkConfigReconciler for
// cra-grout. Like VSR, grout owns its fast-path FIB, so routed CNI attachments
// (recorded in NodeRoutedPorts) are merged into the config before rendering.
func NewNodeNetworkConfigReconciler(
	craManager *cra.Manager,
	clusterClient client.Client,
	logger logr.Logger,
	nodeNetworkConfigPath string,
) (*NodeNetworkConfigReconciler, error) {
	baseConfig, err := config.LoadBaseConfig(baseConfigPath)
	if err != nil {
		return nil, fmt.Errorf("error loading base config: %w", err)
	}

	configApplier := &CRAGroutConfigApplier{
		craManager:  craManager,
		baseConfig:  baseConfig,
		frrTemplate: crafrr.FRRTemplate{FRRTemplatePath: frrTemplatePath},
	}

	commonReconciler, err := common.NewNodeNetworkConfigReconciler(
		clusterClient,
		logger,
		configApplier,
		nodeNetworkConfigPath,
		common.ReconcilerOptions{
			// grout, like VSR, cannot roll back a partially-applied fast-path
			// config to the previous state, so do not restore on failure.
			RestoreOnReconcileFailure: false,
			LocalASN:                  baseConfig.LocalASN,
			// Merge routed CNI attachments (recorded in the node's
			// NodeRoutedPorts object) into the config before rendering: grout's
			// FIB is owned by the agent/fast path, not the CNI.
			RoutedPortsSource: routedcni.NewNodeSource(clusterClient, os.Getenv(healthcheck.NodenameEnv)),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("error creating common reconciler: %w", err)
	}

	return &NodeNetworkConfigReconciler{
		NodeNetworkConfigReconciler: commonReconciler,
	}, nil
}
