package kubevirthook

import (
	"context"

	"google.golang.org/grpc"

	"github.com/telekom/das-schiff-network-operator/pkg/kubevirthook/pb"
)

// HookName identifies this sidecar to virt-launcher.
const HookName = "vhostuser"

// hookVersion is the Callbacks service version served. virt-launcher picks the
// newest version it and the sidecar have in common, preferring v1alpha3.
const hookVersion = "v1alpha3"

// onDefineDomainHookPoint is the hook point name virt-launcher matches on. The
// literal is part of the contract (hooks/info.OnDefineDomainHookPointName).
const onDefineDomainHookPointName = "OnDefineDomain"

// Server serves the two gRPC services virt-launcher expects from a hook
// sidecar: Info, which advertises what the sidecar implements, and the
// versioned Callbacks service that does the work.
type Server struct {
	pb.UnimplementedInfoServer
	pb.UnimplementedCallbacksServer

	hook *Hook
	// stop is closed by Shutdown so the process can exit with its VM.
	stop chan struct{}
}

// NewServer returns a Server serving the given hook.
func NewServer(hook *Hook) *Server {
	return &Server{hook: hook, stop: make(chan struct{})}
}

// Register wires the server into a gRPC server.
func (s *Server) Register(grpcServer *grpc.Server) {
	pb.RegisterInfoServer(grpcServer, s)
	pb.RegisterCallbacksServer(grpcServer, s)
}

// Stopped is closed once virt-launcher has asked the sidecar to shut down.
func (s *Server) Stopped() <-chan struct{} { return s.stop }

// Info advertises the hook points this sidecar subscribes to. Only
// OnDefineDomain is advertised: a sidecar is invoked for every hook point it
// lists, and an unimplemented callback would fail the VM.
func (*Server) Info(context.Context, *pb.InfoParams) (*pb.InfoResult, error) {
	return &pb.InfoResult{
		Name:     HookName,
		Versions: []string{hookVersion},
		HookPoints: []*pb.HookPoint{
			{Name: onDefineDomainHookPointName},
		},
	}, nil
}

// OnDefineDomain returns the converted domain. The result must be the complete
// document: virt-launcher replaces the domain it holds with it, and feeds it to
// the next sidecar in the chain.
func (s *Server) OnDefineDomain(ctx context.Context, params *pb.OnDefineDomainParams) (*pb.OnDefineDomainResult, error) {
	domainXML, err := s.hook.OnDefineDomain(ctx, params.GetDomainXML(), params.GetVmi())
	if err != nil {
		return nil, err
	}
	return &pb.OnDefineDomainResult{DomainXML: domainXML}, nil
}

// PreCloudInitIso is not subscribed to and is served only to satisfy the
// v1alpha3 service definition.
func (*Server) PreCloudInitIso(_ context.Context, params *pb.PreCloudInitIsoParams) (*pb.PreCloudInitIsoResult, error) {
	return &pb.PreCloudInitIsoResult{
		CloudInitNoCloudSource: params.GetCloudInitNoCloudSource(),
		CloudInitData:          params.GetCloudInitData(),
	}, nil
}

// Shutdown lets virt-launcher stop the sidecar when the VM goes away.
func (s *Server) Shutdown(context.Context, *pb.ShutdownParams) (*pb.ShutdownResult, error) {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	return &pb.ShutdownResult{}, nil
}
