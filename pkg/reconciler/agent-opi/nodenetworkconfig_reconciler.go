package agent_opi

import (
	"context"
	"fmt"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	godpunet "github.com/opiproject/godpu/network"
)

type NodeNetworkConfigReconciler struct {
	client                client.Client
	logger                logr.Logger
	healthChecker         *healthcheck.HealthChecker
	NodeNetworkConfig     *v1alpha1.NodeNetworkConfig
	NodeNetworkConfigPath string
}

type reconcileNodeNetworkConfig struct {
	*NodeNetworkConfigReconciler
	logr.Logger
}

func NewNodeNetworkConfigReconciler(clusterClient client.Client, logger logr.Logger, nodeNetworkConfigPath string) (*NodeNetworkConfigReconciler, error) {
	reconciler := &NodeNetworkConfigReconciler{
		client:                clusterClient,
		logger:                logger,
		NodeNetworkConfigPath: nodeNetworkConfigPath,
	}

	return reconciler, nil
}

func (r *reconcileNodeNetworkConfig) fetchNodeConfig(ctx context.Context) (*v1alpha1.NodeNetworkConfig, error) {
	cfg := &v1alpha1.NodeNetworkConfig{}
	err := r.client.Get(ctx, types.NamespacedName{Name: os.Getenv(healthcheck.NodenameEnv)}, cfg)
	if err != nil {
		return nil, fmt.Errorf("error getting NodeConfig: %w", err)
	}
	return cfg, nil
}

func (reconciler *NodeNetworkConfigReconciler) Reconcile(ctx context.Context) error {
	r := &reconcileNodeNetworkConfig{
		NodeNetworkConfigReconciler: reconciler,
		Logger:                      reconciler.logger,
	}

	dpuAddress := os.Getenv("DPU_ADDRESS")

	cfg, err := r.fetchNodeConfig(ctx)
	if err != nil {
		// discard IsNotFound error
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	bridgeClient, err := godpunet.NewLogicalBridge(dpuAddress)
	if err != nil {
		return fmt.Errorf("error creating bridge client: %w", err)
	}

	sviClient, err := godpunet.NewSVI(dpuAddress)
	if err != nil {
		return fmt.Errorf("error creating svi client: %w", err)
	}

	vrfClient, err := godpunet.NewVRF(dpuAddress)
	if err != nil {
		return fmt.Errorf("error creating vrf client: %w", err)
	}

	return nil
}
