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

package workloadcni

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni/pb"
)

const (
	// socketDirPerm restricts the directory holding the unix socket to root, so
	// unprivileged local processes cannot traverse to (and dial) the socket. The
	// workload CNI plugin is executed by the container runtime as root.
	socketDirPerm = 0o700
	// socketPerm restricts the unix socket itself to its owner. The gRPC surface
	// mutates NodeWorkloadPorts and therefore the node's routing state, so it must
	// not be reachable by unprivileged local clients (which the process umask
	// alone does not guarantee).
	socketPerm = 0o600
	// kernelIfNameLen is the kernel IFNAMSIZ-1 interface-name limit.
	kernelIfNameLen = 15
	// maxInterfaceNameLen bounds a routed CRA-side interface name. VSR references
	// it as infra-<ifname>, and that reference must itself fit the kernel limit,
	// so the name has that much less room. Mirrors the CRD's MaxLength.
	maxInterfaceNameLen = kernelIfNameLen - len(InfraPortPrefix)
	// maxRecvMsgSize bounds a single request so a local client cannot exhaust the
	// agent's memory. Routed-port requests are a few hundred bytes at most.
	maxRecvMsgSize = 64 * 1024
)

// Server is the node-local gRPC service the workload CNI plugin calls on ADD/DEL.
// It persists attachments into the node's NodeWorkloadPorts object (the durable
// source of truth); the CRA agent reconciles that object into the datapath.
type Server struct {
	pb.UnimplementedWorkloadCNIServer
	client   client.Client
	nodeName string
	log      logr.Logger
}

// NewServer builds a workload-cni gRPC server for the given node.
func NewServer(c client.Client, nodeName string, log logr.Logger) *Server {
	return &Server{client: c, nodeName: nodeName, log: log}
}

// Serve listens on the unix socket at socketPath until ctx is done. An empty
// socketPath uses DefaultSocketPath.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), socketDirPerm); err != nil {
		return fmt.Errorf("creating socket dir: %w", err)
	}
	// MkdirAll is a no-op on an existing (possibly world-traversable) directory,
	// so tighten it explicitly.
	if err := os.Chmod(filepath.Dir(socketPath), socketDirPerm); err != nil {
		return fmt.Errorf("restricting socket dir: %w", err)
	}
	// Remove a stale socket left by a previous run so Listen can bind. Only ever
	// unlink an actual socket: the path is operator-supplied, and blindly
	// removing it would delete whatever it happens to point at. Lstat (not Stat)
	// so a symlink is reported as a symlink and refused rather than followed.
	switch fi, serr := os.Lstat(socketPath); {
	case serr != nil && !os.IsNotExist(serr):
		return fmt.Errorf("inspecting socket path %s: %w", socketPath, serr)
	case serr == nil && fi.Mode()&os.ModeSocket == 0:
		return fmt.Errorf("refusing to remove %s: not a unix socket (mode %s)", socketPath, fi.Mode())
	case serr == nil:
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale socket %s: %w", socketPath, err)
		}
	}

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, socketPerm); err != nil {
		_ = lis.Close()
		return fmt.Errorf("restricting socket %s: %w", socketPath, err)
	}

	grpcSrv := grpc.NewServer(grpc.MaxRecvMsgSize(maxRecvMsgSize))
	pb.RegisterWorkloadCNIServer(grpcSrv, s)

	go func() {
		<-ctx.Done()
		grpcSrv.GracefulStop()
	}()

	s.log.Info("workload-cni gRPC server listening", "socket", socketPath, "node", s.nodeName)
	if err := grpcSrv.Serve(lis); err != nil {
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

// Add records (upserts) a routed attachment.
func (s *Server) Add(ctx context.Context, req *pb.AddRequest) (*pb.AddResponse, error) {
	entry, err := entryFromRequest(req)
	if err != nil {
		return nil, err
	}

	if err := s.mutate(ctx, func(spec *v1alpha1.NodeWorkloadPortsSpec) bool {
		return UpsertEntry(spec, entry)
	}); err != nil {
		return nil, fmt.Errorf("recording workload port: %w", err)
	}
	s.log.Info("recorded workload port", "container", entry.ContainerID, "interface", entry.Interface, "vrf", entry.VRF)
	return &pb.AddResponse{}, nil
}

// entryFromRequest validates an ADD request and converts it into the entry
// persisted on NodeWorkloadPorts. Everything reaching the datapath (interface
// name, gateways, host routes) is validated here so a malformed request fails
// with a clear InvalidArgument rather than an opaque CRD validation or renderer
// error later on.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func entryFromRequest(req *pb.AddRequest) (*v1alpha1.WorkloadPortEntry, error) {
	port := req.GetPort()
	if port == nil || port.GetInterface() == "" {
		return nil, status.Error(codes.InvalidArgument, "interface is required")
	}
	if len(port.GetInterface()) > maxInterfaceNameLen {
		return nil, status.Errorf(codes.InvalidArgument,
			"interface %q exceeds %d characters", port.GetInterface(), maxInterfaceNameLen)
	}
	if req.GetContainerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "container_id is required")
	}
	// NodeWorkloadPorts requires these, so reject them here rather than letting the
	// write fail later with an opaque CRD validation error.
	if req.GetPodNamespace() == "" {
		return nil, status.Error(codes.InvalidArgument, "pod_namespace is required")
	}
	if req.GetPodName() == "" {
		return nil, status.Error(codes.InvalidArgument, "pod_name is required")
	}
	if err := validateGateway("gateway_v4", port.GetGatewayV4(), true); err != nil {
		return nil, err
	}
	if err := validateGateway("gateway_v6", port.GetGatewayV6(), false); err != nil {
		return nil, err
	}
	for _, hr := range port.GetHostRoutes() {
		if err := validateHostRoute(hr); err != nil {
			return nil, err
		}
	}

	return &v1alpha1.WorkloadPortEntry{
		PodNamespace: req.GetPodNamespace(),
		PodName:      req.GetPodName(),
		ContainerID:  req.GetContainerId(),
		VRF:          req.GetVrf(),
		WorkloadPort: v1alpha1.WorkloadPort{
			Interface:  port.GetInterface(),
			GatewayV4:  port.GetGatewayV4(),
			GatewayV6:  port.GetGatewayV6(),
			HostRoutes: port.GetHostRoutes(),
		},
	}, nil
}

// validateGateway checks an optional on-link gateway address: it must be a CIDR
// of the field's own address family, since it is rendered into that family's
// datapath configuration and a mismatch would silently land in the wrong one.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func validateGateway(field, value string, wantV4 bool) error {
	if value == "" {
		return nil
	}
	ip, ipNet, err := net.ParseCIDR(value)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid %s %q", field, value)
	}
	if isV4 := ip.To4() != nil; isV4 != wantV4 {
		family := "IPv6"
		if wantV4 {
			family = "IPv4"
		}
		return status.Errorf(codes.InvalidArgument, "%s %q is not an %s address", field, value, family)
	}
	// The gateway is added verbatim as an address on the CRA-side port, so a
	// shorter prefix would put a whole connected subnet on that interface and
	// leak it into the fabric. Only a single-address prefix is meaningful for
	// an on-link gateway.
	if ones, bits := ipNet.Mask.Size(); ones != bits {
		return status.Errorf(codes.InvalidArgument,
			"%s %q must be a single address (/%d)", field, value, bits)
	}
	return nil
}

// validateHostRoute checks that a workload host route is a single-address
// prefix (/32 or /128). A shorter prefix would advertise a whole subnet toward
// the fabric via the workload's port, which is never intended.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func validateHostRoute(value string) error {
	ip, ipNet, err := net.ParseCIDR(value)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid host route %q", value)
	}
	ones, bits := ipNet.Mask.Size()
	if ones != bits {
		return status.Errorf(codes.InvalidArgument,
			"host route %q must be a single address (/%d)", value, bits)
	}
	if (ip.To4() != nil) != (bits == net.IPv4len*8) {
		return status.Errorf(codes.InvalidArgument, "host route %q has a mismatched prefix length", value)
	}
	return nil
}

// Del removes a routed attachment (idempotent).
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func (s *Server) Del(ctx context.Context, req *pb.DelRequest) (*pb.DelResponse, error) {
	if req.GetContainerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "container_id is required")
	}
	if err := s.mutate(ctx, func(spec *v1alpha1.NodeWorkloadPortsSpec) bool {
		return RemoveEntry(spec, req.GetContainerId(), req.GetInterface())
	}); err != nil {
		return nil, fmt.Errorf("removing workload port: %w", err)
	}
	s.log.Info("removed workload port", "container", req.GetContainerId(), "interface", req.GetInterface())
	return &pb.DelResponse{}, nil
}

// mutate get-or-creates the node's NodeWorkloadPorts object and applies fn to its
// spec. fn returns whether it changed the spec; if not, no write is issued.
//
// Concurrent CNI ADDs on the same node race both on Update (Conflict) and on the
// initial Create (two callers observing NotFound, one losing with AlreadyExists);
// both are retried so the operation is an effective get-or-create.
func (s *Server) mutate(ctx context.Context, fn func(*v1alpha1.NodeWorkloadPortsSpec) bool) error {
	retriable := func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
	}
	if err := retry.OnError(retry.DefaultRetry, retriable, func() error {
		nrp := &v1alpha1.NodeWorkloadPorts{}
		err := s.client.Get(ctx, types.NamespacedName{Name: s.nodeName}, nrp)
		if apierrors.IsNotFound(err) {
			fresh := &v1alpha1.NodeWorkloadPorts{}
			fresh.Name = s.nodeName
			if !fn(&fresh.Spec) {
				return nil
			}
			// Returned as-is (not wrapped) so AlreadyExists stays retriable.
			return s.client.Create(ctx, fresh)
		}
		if err != nil {
			return fmt.Errorf("getting NodeWorkloadPorts: %w", err)
		}
		if !fn(&nrp.Spec) {
			return nil
		}
		// Returned as-is (not wrapped) so Conflict stays retriable.
		return s.client.Update(ctx, nrp)
	}); err != nil {
		return fmt.Errorf("mutating NodeWorkloadPorts %q: %w", s.nodeName, err)
	}
	return nil
}
