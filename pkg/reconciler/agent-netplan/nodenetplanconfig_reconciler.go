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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type NodeNetplanConfigReconciler struct {
	client client.Client
	logger logr.Logger

	netplanClient netplanclient.Client
	healthChecker *healthcheck.HealthChecker
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

	// Load network healthcheck config and create health checker (reuse common file path)
	nc, err := healthcheck.LoadConfig(healthcheck.NetHealthcheckFile)
	if err != nil {
		return nil, fmt.Errorf("error loading networking healthcheck config: %w", err)
	}
	tcpDialer := healthcheck.NewTCPDialer(nc.Timeout)
	reconciler.healthChecker, err = healthcheck.NewHealthChecker(reconciler.client, healthcheck.NewDefaultHealthcheckToolkit(tcpDialer), nc)
	if err != nil {
		return nil, fmt.Errorf("error creating networking healthchecker: %w", err)
	}

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
		_ = reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonNetplanApplyFailed, err.Error())
		return fmt.Errorf("error applying desired state: %w", err)
	}

	// Run basic health checks (interfaces/reachability + API) after applying netplan config
	if err := reconciler.healthChecker.CheckInterfaces(); err != nil {
		_ = reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonInterfaceCheckFailed, err.Error())
		return fmt.Errorf("error checking network interfaces: %w", err)
	}
	if err := reconciler.healthChecker.CheckReachability(); err != nil {
		_ = reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonReachabilityFailed, err.Error())
		return fmt.Errorf("error checking network reachability: %w", err)
	}
	if err := reconciler.healthChecker.CheckAPIServer(ctx); err != nil {
		_ = reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionFalse, healthcheck.ReasonAPIServerFailed, err.Error())
		return fmt.Errorf("error checking API Server reachability: %w", err)
	}
	if err := reconciler.healthChecker.UpdateReadinessCondition(ctx, corev1.ConditionTrue, healthcheck.ReasonHealthChecksPassed, "All network operator health checks passed"); err != nil {
		reconciler.logger.Error(err, "failed to update network operator readiness condition")
	}
	if !reconciler.healthChecker.TaintsRemoved() {
		if err := reconciler.healthChecker.RemoveTaints(ctx); err != nil {
			return fmt.Errorf("error removing taint from the node: %w", err)
		}
	}

	return nil
}
