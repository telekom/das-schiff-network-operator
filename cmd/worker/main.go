package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/go-logr/zapr"
	vrfigbpadapter "github.com/telekom/das-schiff-network-operator/pkg/adapters/vrf_igbp"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/worker"
	workerpb "github.com/telekom/das-schiff-network-operator/pkg/worker/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func main() {
	var agentType string
	var port int
	flag.StringVar(&agentType, "agent", "vrf-igbp", "Use selected agent type (default: vrf-igbp).")
	flag.IntVar(&port, "port", worker.DefaultPort, fmt.Sprintf("gRPC listening port. (default: %d)", worker.DefaultPort))

	zc := zap.NewProductionConfig()
	zc.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	zc.DisableStacktrace = true
	z, _ := zc.Build()
	log := zapr.NewLogger(z)
	log = log.WithName("agent")

	log.Info("agent's port", "port", port)

	anycastTracker := anycast.NewTracker(&nl.Toolkit{})

	var err error
	var adapter worker.Adapter
	switch agentType {
	case "vrf-igbp":
		adapter, err = vrfigbpadapter.New(anycastTracker, log, frr.NewFRRManager(), nl.NewManager(&nl.Toolkit{}))
	default:
		log.Error(fmt.Errorf("agent is currently not supported"), "type", agentType)
		os.Exit(1)
	}

	if err != nil {
		log.Error(err, "error creating adapter")
		os.Exit(1)
	}

	log.Info("created adapter", "type", agentType)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Error(err, "error on listening start")
		os.Exit(1)
	}

	grpcServer := grpc.NewServer([]grpc.ServerOption{}...)
	srv := worker.NewServer(adapter, &log)
	workerpb.RegisterAgentServer(grpcServer, srv)

	log.Info("created server, start listening...")

	if err := grpcServer.Serve(lis); err != nil {
		log.Error(err, "grpc server error")
		os.Exit(1)
	}
}
