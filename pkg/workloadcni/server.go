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
	"strconv"
	"strings"
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
	helpertypes "github.com/telekom/das-schiff-network-operator/pkg/helpers/types"
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

// DefaultLayer2Namespace is the namespace Layer2Attachments are read from when
// the agent is not told otherwise.
const DefaultLayer2Namespace = "default"

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
	// l2Namespace is stamped on Layer2Attachment references sent as bare names.
	l2Namespace string
	// mu serializes the node's NodeWorkloadPorts writes, see mutate.
	mu sync.Mutex
}

// mutateTimeout bounds one NodeWorkloadPorts write. It runs detached from the
// caller's context (see mutate) and must not block the node's writes forever
// on an unreachable apiserver.
const mutateTimeout = 30 * time.Second

// ServerOption customises a Server at construction time.
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

// WithLayer2Namespace sets the namespace Layer2Attachment references are
// resolved in. An empty value keeps the default.
func WithLayer2Namespace(namespace string) ServerOption {
	return func(s *Server) {
		if namespace != "" {
			s.l2Namespace = namespace
		}
	}
}

// NewServer builds a workload-cni gRPC server for the given node.
func NewServer(c client.Client, nodeName string, log logr.Logger, opts ...ServerOption) *Server {
	s := &Server{
		client:       c,
		nodeName:     nodeName,
		log:          log,
		reservedVRFs: map[string]bool{},
		l2Namespace:  DefaultLayer2Namespace,
	}
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

// Add records (upserts) a workload attachment.
func (s *Server) Add(ctx context.Context, req *pb.AddRequest) (*pb.AddResponse, error) {
	entry, err := s.entryFromRequest(req)
	if err != nil {
		return nil, err
	}
	if err := s.checkLayer2Preconditions(ctx, entry); err != nil {
		return nil, err
	}

	if err := s.mutate(ctx, func(spec *v1alpha1.NodeWorkloadPortsSpec) bool {
		return UpsertEntry(spec, entry)
	}); err != nil {
		return nil, fmt.Errorf("recording workload port: %w", err)
	}
	s.log.Info("recorded workload port", "container", entry.ContainerID, "interface", entry.Interface,
		"vrf", entry.VRF, "l2a", layer2AttachmentRefLog(entry.Layer2AttachmentRef),
		"l2Trunk", layer2TrunkLog(entry.Layer2Trunk))
	return &pb.AddResponse{}, nil
}

// checkLayer2Preconditions refuses an L2 attachment that the merge would not be
// able to apply against the node's current NodeNetworkConfig: a Layer2Attachment
// reference that does not resolve, or an MTU no referenced domain can carry.
// The merge would otherwise record the entry and silently skip it on every
// reconcile, leaving the pod running with a dead port; failing the ADD instead
// makes the runtime retry the sandbox until the L2A pipeline has stamped the
// domain onto the node (or the operator notices the FailedPrecondition).
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func (s *Server) checkLayer2Preconditions(ctx context.Context, entry *v1alpha1.WorkloadPortEntry) error {
	if entry.Layer2AttachmentRef == nil && len(entry.Layer2Trunk) == 0 {
		return nil
	}
	cfg := &v1alpha1.NodeNetworkConfig{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: s.nodeName}, cfg); err != nil {
		if apierrors.IsNotFound(err) {
			return status.Errorf(codes.FailedPrecondition,
				"node %s has no NodeNetworkConfig yet; an L2 attachment needs its Layer2 domains", s.nodeName)
		}
		return status.Errorf(codes.Unavailable, "reading NodeNetworkConfig %s: %v", s.nodeName, err)
	}
	members, err := resolveLayer2Members(&cfg.Spec, entry)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition,
			"%v (Layer2Attachments are stamped onto the NodeNetworkConfig by the intent reconciler; "+
				"check that the Layer2Attachment exists in namespace %q, selects this node, and that the "+
				"operator runs with --enable-intent-reconciler)", err, s.l2Namespace)
	}
	if err := checkLayer2MTU(&cfg.Spec, entry, members); err != nil {
		return status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	return nil
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
	trunk, err := s.layer2TrunkFromPB(req.GetLayer2Trunk())
	if err != nil {
		return nil, err
	}
	mtu, err := portMTUFromPB(port.GetMtu())
	if err != nil {
		return nil, err
	}
	entry := &v1alpha1.WorkloadPortEntry{
		PodNamespace:        req.GetPodNamespace(),
		PodName:             req.GetPodName(),
		ContainerID:         req.GetContainerId(),
		VRF:                 req.GetVrf(),
		Layer2AttachmentRef: s.layer2AttachmentRefFromPB(req.GetLayer2AttachmentRef()),
		Layer2Trunk:         trunk,
		WorkloadPort: v1alpha1.WorkloadPort{
			Interface:  port.GetInterface(),
			MTU:        mtu,
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

// maxVLANID is the highest assignable 802.1Q VLAN id (4095 is reserved).
const maxVLANID = 4094

// maxLayer2TrunkMembers bounds the members of one trunk. It matches the
// NodeWorkloadPorts schema (MaxItems on layer2Trunk), so an oversized request
// is rejected up front instead of failing the API commit later.
const maxLayer2TrunkMembers = 64

// MaxPortMTU bounds a requested MTU. It matches the NodeNetworkConfig schema.
const MaxPortMTU = 9216

// MinPortMTU is the smallest MTU IPv6 can run on.
const MinPortMTU = 1280

// portMTUFromPB validates the requested MTU and applies the default when the
// request carries none.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func portMTUFromPB(mtu uint32) (uint16, error) {
	if mtu == 0 {
		return DefaultPortMTU, nil
	}
	if mtu < MinPortMTU || mtu > MaxPortMTU {
		return 0, status.Errorf(codes.InvalidArgument, "mtu %d is out of range (%d-%d)",
			mtu, MinPortMTU, MaxPortMTU)
	}
	return uint16(mtu), nil
}

// layer2TrunkFromPB converts the wire trunk members to the API type; the
// semantic checks live in ValidateEntry. Only the VLAN range is checked here
// because the wire carries a uint32 that has to fit the API's uint16.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func (s *Server) layer2TrunkFromPB(members []*pb.Layer2TrunkMember) ([]v1alpha1.Layer2TrunkMember, error) {
	if len(members) == 0 {
		return nil, nil
	}
	out := make([]v1alpha1.Layer2TrunkMember, 0, len(members))
	for _, m := range members {
		ref := s.layer2AttachmentRefFromPB(m.GetRef())
		if ref == nil {
			return nil, status.Error(codes.InvalidArgument, "layer2_trunk member requires ref.name")
		}
		member := v1alpha1.Layer2TrunkMember{Layer2AttachmentRef: *ref}
		if vlan := m.GetVlan(); vlan != 0 {
			if vlan > maxVLANID {
				return nil, status.Errorf(codes.InvalidArgument,
					"layer2_trunk member %q has an invalid vlan %d (want 1-%d)", ref.Name, vlan, maxVLANID)
			}
			member.VLAN = helpertypes.ToPtr(uint16(vlan))
		}
		out = append(out, member)
	}
	return out, nil
}

// layer2AttachmentRefFromPB converts the wire L2 reference to the API type.
// The wire carries a bare name, so the server stamps its configured namespace.
func (s *Server) layer2AttachmentRefFromPB(ref *pb.Layer2AttachmentRef) *v1alpha1.Layer2AttachmentRef {
	if ref == nil {
		return nil
	}
	return &v1alpha1.Layer2AttachmentRef{
		Name:      ref.GetName(),
		Namespace: s.l2Namespace,
	}
}

// layer2AttachmentRefLog renders an L2 reference for diagnostic messages.
func layer2AttachmentRefLog(ref *v1alpha1.Layer2AttachmentRef) string {
	if ref == nil {
		return ""
	}
	return ref.Namespace + "/" + ref.Name
}

// layer2TrunkLog renders trunk members as "<namespace>/<name>@<vlan>", with
// "@auto" for a member that inherits the Layer2 domain's VLAN id.
func layer2TrunkLog(members []v1alpha1.Layer2TrunkMember) string {
	if len(members) == 0 {
		return ""
	}
	parts := make([]string, 0, len(members))
	for i := range members {
		vlan := "auto"
		if members[i].VLAN != nil {
			vlan = strconv.Itoa(int(*members[i].VLAN))
		}
		parts = append(parts, layer2AttachmentRefLog(&members[i].Layer2AttachmentRef)+"@"+vlan)
	}
	return strings.Join(parts, ",")
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
