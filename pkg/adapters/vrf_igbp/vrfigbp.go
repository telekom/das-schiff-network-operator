package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/agent"
	agentpb "github.com/telekom/das-schiff-network-operator/pkg/agent/pb"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultTimeout = 30 * time.Second

type VrfIgbp struct {
	netlinkManager *nl.Manager
	config         *config.Config
	frrManager     *frr.Manager
	anycastTracker *anycast.Tracker
	dirtyFRRConfig bool
	healthChecker  *healthcheck.HealthChecker
	logger         logr.Logger
}

func New(anycastTracker *anycast.Tracker, logger logr.Logger) (agent.Adapter, error) {
	reconciler := &VrfIgbp{
		netlinkManager: nl.NewManager(&nl.Toolkit{}),
		frrManager:     frr.NewFRRManager(),
		anycastTracker: anycastTracker,
		logger:         logger,
	}

	if val := os.Getenv("FRR_CONFIG_FILE"); val != "" {
		reconciler.frrManager.ConfigPath = val
	}
	if err := reconciler.frrManager.Init(); err != nil {
		return nil, fmt.Errorf("error trying to init FRR Manager: %w", err)
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	reconciler.config = cfg

	nc, err := healthcheck.LoadConfig(healthcheck.NetHealthcheckFile)
	if err != nil {
		return nil, fmt.Errorf("error loading networking healthcheck config: %w", err)
	}

	reconciler.healthChecker, err = healthcheck.NewHealthChecker(nil,
		healthcheck.NewDefaultHealthcheckToolkit(reconciler.frrManager, nil),
		nc)
	if err != nil {
		return nil, fmt.Errorf("error creating netwokring healthchecker: %w", err)
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
	grpcClient agentpb.AgentClient
}

func NewClient(address string) (agent.Client, error) {
	var grpcOpts []grpc.DialOption
	grpcOpts = append(grpcOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(address, grpcOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to create gRPC connection: %w", err)
	}

	client := agentpb.NewAgentClient(conn)

	vrfigbpClient := vrfIgbpClient{
		grpcClient: client,
	}

	return &vrfigbpClient, nil
}

func (c *vrfIgbpClient) SendConfig(ctx context.Context, nodeConfig *v1alpha1.NodeConfig) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	nc := agentpb.NetworkConfiguration{
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
