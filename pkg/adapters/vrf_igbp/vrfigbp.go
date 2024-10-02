package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/worker"
	workerpb "github.com/telekom/das-schiff-network-operator/pkg/worker/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultTimeout = 30 * time.Second

type VrfIgbp struct {
	netlinkManager *nl.Manager
	config         *config.Config
	frrManager     frr.ManagerInterface
	anycastTracker *anycast.Tracker
	dirtyFRRConfig bool
	healthChecker  healthcheck.Adapter
	logger         logr.Logger
}

func New(anycastTracker *anycast.Tracker, logger logr.Logger, frrManager frr.ManagerInterface, netlinkManager *nl.Manager) (worker.Adapter, error) {
	reconciler := &VrfIgbp{
		netlinkManager: netlinkManager,
		frrManager:     frrManager,
		anycastTracker: anycastTracker,
		logger:         logger,
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	reconciler.config = cfg

	if val := os.Getenv("FRR_CONFIG_FILE"); val != "" {
		reconciler.frrManager.SetConfigPath(val)
	}

	if err := reconciler.frrManager.Init(cfg.SkipVRFConfig[0]); err != nil {
		return nil, fmt.Errorf("error trying to init FRR Manager: %w", err)
	}

	nc, err := healthcheck.LoadConfig(healthcheck.NetHealthcheckFile)
	if err != nil {
		return nil, fmt.Errorf("error loading networking healthcheck config: %w", err)
	}

	tcpDialer := healthcheck.NewTCPDialer(nc.Timeout)
	reconciler.healthChecker, err = healthcheck.NewHealthChecker(nil,
		healthcheck.NewDefaultHealthcheckToolkit(reconciler.frrManager, tcpDialer),
		nc)
	if err != nil {
		return nil, fmt.Errorf("error creating networking healthchecker: %w", err)
	}

	return reconciler, nil
}

func (r *VrfIgbp) CheckHealth() error {
	if _, err := r.healthChecker.IsFRRActive(); err != nil {
		return fmt.Errorf("error checking FRR status: %w", err)
	}
	if err := r.healthChecker.CheckInterfaces(); err != nil {
		return fmt.Errorf("error checking network interfaces: %w", err)
	}
	return nil
}

func (r *VrfIgbp) GetConfig() *config.Config {
	return r.config
}

type vrfIgbpClient struct {
	grpcClient workerpb.AgentClient
}

func NewClient(address string) (worker.Client, error) {
	var grpcOpts []grpc.DialOption
	grpcOpts = append(grpcOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(address, grpcOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to create gRPC connection: %w", err)
	}

	client := workerpb.NewAgentClient(conn)

	vrfigbpClient := vrfIgbpClient{
		grpcClient: client,
	}

	return &vrfigbpClient, nil
}

func (c *vrfIgbpClient) SendConfig(ctx context.Context, nodeConfig *v1alpha1.NodeNetworkConfig) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	nc := workerpb.NetworkConfiguration{
		Data: []byte{},
	}
	data, err := json.Marshal(*nodeConfig)
	if err != nil {
		return fmt.Errorf("error marshaling NodeConfig: %w", err)
	}

	nc.Data = data

	if _, err = c.grpcClient.SetConfiguration(timeoutCtx, &nc); err != nil {
		return fmt.Errorf("error setting configuration: %w", err)
	}

	return nil
}
