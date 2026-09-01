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
	"strconv"
	"strings"

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
	// l2Namespace is stamped on Layer2Attachment references sent as bare names.
	l2Namespace string
}

// ServerOption customises a Server at construction time.
type ServerOption func(*Server)

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
	s := &Server{client: c, nodeName: nodeName, log: log, l2Namespace: DefaultLayer2Namespace}
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
	entry, err := s.entryFromRequest(req)
	if err != nil {
		return nil, err
	}

	if err := s.mutate(ctx, func(spec *v1alpha1.NodeWorkloadPortsSpec) bool {
		return UpsertEntry(spec, entry)
	}); err != nil {
		return nil, fmt.Errorf("recording workload port: %w", err)
	}
	s.log.Info("recorded workload port", "container", entry.ContainerID, "interface", entry.Interface,
		"vrf", entry.VRF, "transport", entry.Transport, "l2a", layer2AttachmentRefLog(entry.Layer2AttachmentRef),
		"l2Trunk", layer2TrunkLog(entry.Layer2Trunk))
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
	if port == nil || port.GetInterface() == "" {
		return nil, status.Error(codes.InvalidArgument, "interface is required")
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
	transport, err := validateWiring(port)
	if err != nil {
		return nil, err
	}
	if err := validateInterfaceName(port.GetInterface()); err != nil {
		return nil, err
	}
	l2aRef := s.layer2AttachmentRefFromPB(req.GetLayer2AttachmentRef())
	trunk, err := s.layer2TrunkFromPB(req.GetLayer2Trunk())
	if err != nil {
		return nil, err
	}
	if err := validateL2Attach(req, l2aRef, trunk); err != nil {
		return nil, err
	}
	if err := validateTrunkSubinterfaceNames(port.GetInterface(), trunk); err != nil {
		return nil, err
	}
	mtu, err := portMTUFromPB(port.GetMtu())
	if err != nil {
		return nil, err
	}

	return &v1alpha1.WorkloadPortEntry{
		PodNamespace:        req.GetPodNamespace(),
		PodName:             req.GetPodName(),
		ContainerID:         req.GetContainerId(),
		VRF:                 req.GetVrf(),
		Layer2AttachmentRef: l2aRef,
		Layer2Trunk:         trunk,
		WorkloadPort: v1alpha1.WorkloadPort{
			Interface: port.GetInterface(),
			PortWiring: v1alpha1.PortWiring{
				Transport:  transport,
				SocketPath: port.GetSocketPath(),
				SocketMode: port.GetSocketMode(),
			},
			MTU:        mtu,
			GatewayV4:  port.GetGatewayV4(),
			GatewayV6:  port.GetGatewayV6(),
			HostRoutes: port.GetHostRoutes(),
		},
	}, nil
}

// maxVLANID is the highest assignable 802.1Q VLAN id (4095 is reserved).
const maxVLANID = 4094

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

// layer2TrunkFromPB converts and validates the wire trunk members. Members
// that inherit their VLAN id (0 on the wire) can only be checked for collisions
// once the referenced Layer2 is known, which happens at merge time.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func (s *Server) layer2TrunkFromPB(members []*pb.Layer2TrunkMember) ([]v1alpha1.Layer2TrunkMember, error) {
	if len(members) == 0 {
		return nil, nil
	}
	out := make([]v1alpha1.Layer2TrunkMember, 0, len(members))
	seenRefs := make(map[string]struct{}, len(members))
	seenVLANs := make(map[uint32]struct{}, len(members))
	for _, m := range members {
		ref := s.layer2AttachmentRefFromPB(m.GetRef())
		if ref == nil || ref.Name == "" {
			return nil, status.Error(codes.InvalidArgument, "layer2_trunk member requires ref.name")
		}
		if _, dup := seenRefs[ref.Name]; dup {
			return nil, status.Errorf(codes.InvalidArgument,
				"layer2_trunk references %q more than once", ref.Name)
		}
		seenRefs[ref.Name] = struct{}{}

		member := v1alpha1.Layer2TrunkMember{Layer2AttachmentRef: *ref}
		if vlan := m.GetVlan(); vlan != 0 {
			if vlan > maxVLANID {
				return nil, status.Errorf(codes.InvalidArgument,
					"layer2_trunk member %q has an invalid vlan %d (want 1-%d)", ref.Name, vlan, maxVLANID)
			}
			if _, dup := seenVLANs[vlan]; dup {
				return nil, status.Errorf(codes.InvalidArgument,
					"layer2_trunk uses vlan %d more than once", vlan)
			}
			seenVLANs[vlan] = struct{}{}
			member.VLAN = helpertypes.ToPtr(uint16(vlan))
		}
		out = append(out, member)
	}
	return out, nil
}

// validateL2Attach enforces the mutual exclusion between L2 attach mode and
// routed fields, and between the access and trunk forms of L2 attach.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func validateL2Attach(req *pb.AddRequest, ref *v1alpha1.Layer2AttachmentRef, trunk []v1alpha1.Layer2TrunkMember) error {
	if ref == nil && len(trunk) == 0 {
		return nil
	}
	if ref != nil && len(trunk) > 0 {
		return status.Error(codes.InvalidArgument,
			"layer2_attachment_ref (untagged access port) and layer2_trunk (tagged trunk) are mutually exclusive")
	}
	if ref != nil && ref.Name == "" {
		return status.Error(codes.InvalidArgument, "layer2_attachment_ref.name is required")
	}
	port := req.GetPort()
	if req.GetVrf() != "" || port.GetGatewayV4() != "" || port.GetGatewayV6() != "" || len(port.GetHostRoutes()) > 0 {
		return status.Error(codes.InvalidArgument,
			"L2 attach mode is mutually exclusive with vrf, gateways and host routes")
	}
	return nil
}

// validateTrunkSubinterfaceNames verifies that every VLAN device the request
// can name fits the VSR and Linux interface limit. Inherited VLANs are not
// known until the node configuration merge, so they reserve the largest
// assignable suffix; explicit VLANs use their actual decimal length.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func validateTrunkSubinterfaceNames(ifName string, members []v1alpha1.Layer2TrunkMember) error {
	for i := range members {
		vlan := uint16(maxVLANID)
		if members[i].VLAN != nil {
			vlan = *members[i].VLAN
		}
		subinterface := fmt.Sprintf("%s.%d", ifName, vlan)
		if len(subinterface) > kernelIfNameLen {
			return status.Errorf(codes.InvalidArgument,
				"layer2_trunk member %q creates sub-interface %q exceeding %d characters",
				members[i].Name, subinterface, kernelIfNameLen)
		}
	}
	return nil
}

// validateInterfaceName bounds the bare CRA-side VSR interface name. Its
// infra-<ifname> and fpvhost-<ifname> references do not consume this budget.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func validateInterfaceName(ifName string) error {
	if len(ifName) > maxInterfaceNameLen {
		return status.Errorf(codes.InvalidArgument,
			"interface %q exceeds %d characters", ifName, maxInterfaceNameLen)
	}
	return nil
}

// validateWiring resolves the requested transport and enforces that the
// vhost-user socket fields are present exactly when they are meaningful.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func validateWiring(port *pb.WorkloadPort) (v1alpha1.PortTransport, error) {
	transport := v1alpha1.PortTransport(port.GetTransport())
	if port.GetTransport() == "" {
		transport = v1alpha1.PortTransportVeth
	}

	switch transport {
	case v1alpha1.PortTransportVeth:
		if port.GetSocketPath() != "" || port.GetSocketMode() != "" {
			return "", status.Errorf(codes.InvalidArgument,
				"socket_path and socket_mode are only valid for transport %q", v1alpha1.PortTransportVhostUser)
		}
	case v1alpha1.PortTransportVhostUser:
		if port.GetSocketPath() == "" {
			return "", status.Errorf(codes.InvalidArgument,
				"socket_path is required for transport %q", v1alpha1.PortTransportVhostUser)
		}
		if m := port.GetSocketMode(); m != v1alpha1.SocketModeClient && m != v1alpha1.SocketModeServer {
			return "", status.Errorf(codes.InvalidArgument,
				"invalid socket_mode %q (want %q or %q)", m, v1alpha1.SocketModeClient, v1alpha1.SocketModeServer)
		}
	default:
		return "", status.Errorf(codes.InvalidArgument,
			"invalid transport %q (want %q or %q)",
			port.GetTransport(), v1alpha1.PortTransportVeth, v1alpha1.PortTransportVhostUser)
	}

	return transport, nil
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
