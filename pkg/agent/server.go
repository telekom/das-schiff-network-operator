package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	agentpb "github.com/telekom/das-schiff-network-operator/pkg/agent/pb"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	DefaultPort = 50042
)

type Server struct {
	adapter Adapter
	agentpb.UnimplementedAgentServer
	logger *logr.Logger
}

type Adapter interface {
	ReconcileLayer3([]v1alpha1.VRFRouteConfigurationSpec, []v1alpha1.RoutingTableSpec) error
	ReconcileLayer2([]v1alpha1.Layer2NetworkConfigurationSpec) error
	CheckHealth() error
	GetConfig() *config.Config
}

type Client interface {
	SendConfig(context.Context, *v1alpha1.NodeConfig) error
}

func NewServer(adapter Adapter, logger *logr.Logger) *Server {
	sLog := logger.WithName("agent-server")
	return &Server{
		adapter: adapter,
		logger:  &sLog,
	}
}

// nolint: wrapcheck
func (s Server) SetConfiguration(_ context.Context, nc *agentpb.NetworkConfiguration) (*emptypb.Empty, error) {
	s.logger.Info("new request")
	if nc == nil {
		s.logger.Info("nil request")
		return &emptypb.Empty{}, status.Error(http.StatusBadRequest, "got nil request")
	}

	var nodeCfg v1alpha1.NodeConfig

	s.logger.Info("umarshaling data...")
	if err := json.Unmarshal(nc.Data, &nodeCfg); err != nil {
		s.logger.Error(fmt.Errorf("error unmrashalling NodeConfig objec"), "error")
		return &emptypb.Empty{}, status.Error(http.StatusInternalServerError, "error unmrashalling NodeConfig object")
	}

	if err := s.adapter.GetConfig().ReloadConfig(); err != nil {
		s.logger.Error(fmt.Errorf("error reloading configc"), "error")
		return &emptypb.Empty{}, status.Error(http.StatusInternalServerError, "error reloading config")
	}

	s.logger.Info("reconciling config...", "config", nodeCfg)

	l3vnis := nodeCfg.Spec.Vrf
	l2vnis := nodeCfg.Spec.Layer2
	taas := nodeCfg.Spec.RoutingTable

	if err := s.adapter.ReconcileLayer3(l3vnis, taas); err != nil {
		return &emptypb.Empty{}, status.Errorf(http.StatusInternalServerError, "error configuring Layer3: %s", err.Error())
	}
	if err := s.adapter.ReconcileLayer2(l2vnis); err != nil {
		return &emptypb.Empty{}, status.Errorf(http.StatusInternalServerError, "error configuring Layer2: %s", err.Error())
	}
	if err := s.adapter.CheckHealth(); err != nil {
		return &emptypb.Empty{}, status.Errorf(http.StatusInternalServerError, "healthcheck error: %s", err.Error())
	}

	s.logger.Info("config reconciled successfully")

	return &emptypb.Empty{}, nil
}
