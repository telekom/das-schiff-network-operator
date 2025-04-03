package agent_netplan //nolint:revive

import (
	"context"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/network/net"
	netplanclient "github.com/telekom/das-schiff-network-operator/pkg/network/netplan/client"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan/client/dbus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type NodeNetplanConfigReconciler struct {
	client client.Client
	logger logr.Logger

	netplanClient netplanclient.Client
}

type reconcileNodeNetworkConfig struct {
	*NodeNetplanConfigReconciler
	logr.Logger
}

func NewNodeNetplanConfigReconciler(clusterClient client.Client, logger logr.Logger) (*NodeNetplanConfigReconciler, error) {
	reconciler := &NodeNetplanConfigReconciler{
		client: clusterClient,
		logger: logger,
	}

	netManager := net.NewManager(net.Opts{NetClassPath: "/sys/class/net"})

	netplanOpts := netplanclient.Opts{
		InitialHints: []string{"network"},
		DbusOpts: dbus.Opts{
			SocketPath: "unix:path=/run/dbus/system_bus_socket",
			NetManager: netManager,
		},
	}

	var netplanClient netplanclient.Client
	var err error
	if netplanClient, err = netplanclient.New("20-caas.network", netplanclient.ClientModeDBus, &netplanOpts); err != nil {
		return nil, fmt.Errorf("error creating netplan client: %w", err)
	}

	reconciler.netplanClient = netplanClient

	return reconciler, nil
}

func (r *reconcileNodeNetworkConfig) fetchNodeConfig(ctx context.Context) (*v1alpha1.NodeNetplanConfig, error) {
	cfg := &v1alpha1.NodeNetplanConfig{}
	err := r.client.Get(ctx, types.NamespacedName{Name: os.Getenv(healthcheck.NodenameEnv)}, cfg)
	if err != nil {
		return nil, fmt.Errorf("error getting NodeConfig: %w", err)
	}
	return cfg, nil
}

func (reconciler *NodeNetplanConfigReconciler) Reconcile(ctx context.Context) error {
	r := &reconcileNodeNetworkConfig{
		NodeNetplanConfigReconciler: reconciler,
		Logger:                      reconciler.logger,
	}

	cfg, err := r.fetchNodeConfig(ctx)
	if err != nil {
		// discard IsNotFound error
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	netplanConfig, err := reconciler.netplanClient.Initialize()
	if err != nil {
		return fmt.Errorf("error initializing netplan client: %w", err)
	}
	if err := netplanConfig.Set(&cfg.Spec.DesiredState); err != nil {
		return fmt.Errorf("error setting desired state: %w", err)
	}

	if err := netplanConfig.Apply(); err != nil {
		return fmt.Errorf("error applying desired state: %w", err)
	}

	return nil
}
