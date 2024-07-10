package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/agent"
	agentpb "github.com/telekom/das-schiff-network-operator/pkg/agent/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultTimeout = 30 * time.Second

type Client struct {
	agentpb.AgentClient
}

func NewClient(address string) (agent.Client, error) {
	var grpcOpts []grpc.DialOption
	grpcOpts = append(grpcOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(address, grpcOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to create gRPC connection: %w", err)
	}

	vrfigbpClient := Client{agentpb.NewAgentClient(conn)}

	return &vrfigbpClient, nil
}

func (c *Client) SendConfig(ctx context.Context, nodeConfig *v1alpha1.NodeConfig) error {
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

	if _, err = c.SetConfiguration(timeoutCtx, &nc); err != nil {
		return fmt.Errorf("error setting configuration: %w", err)
	}

	return nil
}
