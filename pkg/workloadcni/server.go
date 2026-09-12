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
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	// maxInterfaceNameLen bounds the bare CRA-side interface name. The VSR
	// resolves infra-<ifname> through a veth's ifalias, which is not limited by
	// IFNAMSIZ. Mirrors the CRD's MaxLength.
	maxInterfaceNameLen = kernelIfNameLen
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
	// reservedVRFs are platform-owned VRF names (management, cluster) a
	// workload port must never be placed into.
	reservedVRFs map[string]bool
	// mu serializes the node's NodeWorkloadPorts writes, see mutate.
	mu sync.Mutex
}

// mutateTimeout bounds one NodeWorkloadPorts write. It runs detached from the
// caller's context (see mutate) and must not block the node's writes forever
// on an unreachable apiserver.
const mutateTimeout = 30 * time.Second

// ServerOption customises a Server.
type ServerOption func(*Server)

// WithReservedVRFs names the platform-owned VRFs (management and cluster VRF
// of the CRA base config) an ADD may not target. Their names are not part of
// the NodeNetworkConfig, so without this the merge would treat them like any
// unknown name and create — or worse, adopt — a local VRF of that name, wiring
// a tenant port into the platform's own routing table.
func WithReservedVRFs(names ...string) ServerOption {
	return func(s *Server) {
		for _, name := range names {
			if name != "" {
				s.reservedVRFs[name] = true
			}
		}
	}
}

// NewServer builds a workload-cni gRPC server for the given node.
func NewServer(c client.Client, nodeName string, log logr.Logger, opts ...ServerOption) *Server {
	s := &Server{client: c, nodeName: nodeName, log: log, reservedVRFs: map[string]bool{}}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Serve listens on the unix socket at socketPath until ctx is done. An empty
// socketPath uses DefaultSocketPath.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	// The directory is created and chmod'ed below; with a relative path
	// filepath.Dir could resolve to "." and the agent would lock down its own
	// working directory instead of the socket directory.
	if !filepath.IsAbs(socketPath) {
		return fmt.Errorf("socket path %q must be absolute", socketPath)
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
	// Serve returns nil once GracefulStop has been called, except when the stop
	// raced ahead of it (ctx already cancelled): that is a clean shutdown too,
	// not a failure to report to the manager.
	if err := grpcSrv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

// Add records (upserts) a routed attachment.
func (s *Server) Add(ctx context.Context, req *pb.AddRequest) (*pb.AddResponse, error) {
	entry, err := s.entryFromRequest(req)
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
func (s *Server) entryFromRequest(req *pb.AddRequest) (*v1alpha1.WorkloadPortEntry, error) {
	port := req.GetPort()
	if port == nil {
		return nil, status.Error(codes.InvalidArgument, "interface is required")
	}
	entry := &v1alpha1.WorkloadPortEntry{
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
	}
	if err := ValidateEntry(entry, s.reservedVRFs); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return entry, nil
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
// Writes are serialized per server and detached from the caller's context. The
// plugin compensates a failed ADD with a DEL, and the ADD may have failed on
// the plugin's side only (its call timed out while the write was in flight):
// the DEL must then observe the ADD's outcome rather than overtake it, or it
// would report "nothing to remove", the plugin would release the address, and
// the ADD would persist a stale entry with it afterwards. Holding the lock
// until the write has settled — even after the caller gave up — gives the DEL
// exactly that ordering.
//
// Concurrent CNI ADDs on the same node still race the initial Create against
// other writers of the object (one losing with AlreadyExists) and Update
// against them (Conflict); both are retried so the operation is an effective
// get-or-create.
func (s *Server) mutate(ctx context.Context, fn func(*v1alpha1.NodeWorkloadPortsSpec) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mutateTimeout)
	defer cancel()

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
