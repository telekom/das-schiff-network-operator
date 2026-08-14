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
	"unicode"

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
	// maxInterfaceNameLen bounds a veth CRA-side interface name. VSR references
	// it as infra-<ifname>, and that reference must itself fit the kernel limit,
	// so the name has that much less room. Mirrors the CRD's MaxLength.
	maxInterfaceNameLen = kernelIfNameLen - len(InfraPortPrefix)
	// maxVhostInterfaceNameLen bounds a vhost-user CRA-side interface name. Its
	// VSR reference is fpvhost-<ifname>, which is longer than infra-<ifname>, so
	// the name gets correspondingly less room.
	maxVhostInterfaceNameLen = kernelIfNameLen - len(FpvhostPortPrefix)
	// maxRecvMsgSize bounds a single request so a local client cannot exhaust the
	// agent's memory. Routed-port requests are a few hundred bytes at most.
	maxRecvMsgSize = 64 * 1024
)

// DefaultLayer2Namespace is the namespace Layer2Attachments are read from when
// the agent is not told otherwise. It mirrors the operator's --intent-namespace
// default, which scopes the whole intent pipeline to a single namespace.
const DefaultLayer2Namespace = "default"

// Server is the node-local gRPC service the workload CNI plugin calls on ADD/DEL.
// It persists attachments into the node's NodeWorkloadPorts object (the durable
// source of truth); the CRA agent reconciles that object into the datapath.
type Server struct {
	pb.UnimplementedWorkloadCNIServer
	client   client.Client
	nodeName string
	log      logr.Logger
	// l2Namespace is the namespace every Layer2Attachment reference is resolved
	// in. The wire carries bare names, so this is stamped onto the recorded
	// attachment to keep NodeWorkloadPorts references fully qualified.
	l2Namespace string
	// requireGroutTap rejects routed attachments that ask the CNI to create the
	// CRA-side netdev itself. See RequireGroutTap.
	requireGroutTap bool
}

// ServerOption customises a Server at construction time.
type ServerOption func(*Server)

// WithLayer2Namespace sets the namespace Layer2Attachment references are
// resolved in (the agent's --intent-namespace). An empty value keeps the
// default.
func WithLayer2Namespace(namespace string) ServerOption {
	return func(s *Server) {
		if namespace != "" {
			s.l2Namespace = namespace
		}
	}
}

// RequireGroutTap makes the server reject routed attachments that use the veth
// transport, i.e. those where the CNI creates the CRA-side netdev and moves it
// into the CRA netns.
//
// grout cannot adopt a moved-in kernel veth (its edge image carries no
// af_packet/af_xdp/memif PMD), so such a port is rendered as a net_tap of the
// same name instead. grout then fails to create its control-plane tap because
// the veth already owns the name (TUNSETIFF returns EINVAL for a name held by a
// non-tun device) and leaves the interface half-initialised, which crashes the
// dataplane and takes the node's whole configuration with it. Failing the CNI
// ADD keeps the misconfiguration contained to the pod that requested it.
func RequireGroutTap() ServerOption {
	return func(s *Server) { s.requireGroutTap = true }
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
	// tap_name lets the grout-flavor CNI know which net_tap to wait for in the
	// CRA netns and move into the pod. It is deliberately NOT the interface name:
	// grout names its own control plane representor after the interface, so the
	// DPDK tap has to be called something else (see TapIfaceName).
	return &pb.AddResponse{TapName: TapIfaceName(entry.Interface)}, nil
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
	transport, err := s.validateWiring(port)
	if err != nil {
		return nil, err
	}
	if err := validateInterfaceName(port.GetInterface(), transport); err != nil {
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

// MaxPortMTU bounds a requested MTU. It matches the NodeNetworkConfig schema,
// and keeps a request from asking for something no fabric can carry.
const MaxPortMTU = 9216

// MinPortMTU is the smallest MTU IPv6 can run on, and the floor the CNI itself
// would fail on when it configures the veth.
const MinPortMTU = 1280

// portMTUFromPB validates the requested MTU and applies the default when the
// request carries none, so the recorded attachment always states the size the
// datapath has to honour rather than leaving it to each renderer to guess.
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

// layer2TrunkFromPB converts and validates the wire trunk members: every member
// needs a name, an in-range VLAN id, and must reference a distinct
// Layer2Attachment under a distinct workload-side VLAN id. Members that inherit
// their VLAN id (0 on the wire) can only be checked for collisions once the
// referenced Layer2 is known, which happens at merge time.
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

// validateL2Attach enforces the mutual exclusion between L2 attach mode and the
// routed fields, and between the access and trunk forms of it, mirroring the
// CEL rules on NodeWorkloadPorts so a bad request fails with a clear
// InvalidArgument instead of an opaque CRD rejection.
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

// validateInterfaceName bounds the CRA-side interface name so that the VSR port
// reference derived from it (infra-<ifname> or fpvhost-<ifname>) still fits the
// kernel interface-name limit.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func validateInterfaceName(ifName string, transport v1alpha1.PortTransport) error {
	maxLen := maxInterfaceNameLen
	if transport == v1alpha1.PortTransportVhostUser {
		maxLen = maxVhostInterfaceNameLen
	}
	if len(ifName) > maxLen {
		return status.Errorf(codes.InvalidArgument,
			"interface %q exceeds %d characters for transport %q", ifName, maxLen, transport)
	}
	return nil
}

// validateWiring resolves the requested transport and enforces that the
// vhost-user socket fields are present exactly when they are meaningful. An
// unknown transport is rejected rather than silently downgraded to veth, which
// would render a plain infrastructure port for a workload that is waiting on a
// fast-path socket.
//
//nolint:wrapcheck // gRPC status errors are the wire representation and must be returned verbatim.
func (s *Server) validateWiring(port *pb.WorkloadPort) (v1alpha1.PortTransport, error) {
	transport := portTransportFromPB(port.GetTransport())

	switch transport {
	case v1alpha1.PortTransportVeth:
		if port.GetSocketPath() != "" || port.GetSocketMode() != "" {
			return "", status.Errorf(codes.InvalidArgument,
				"socket_path and socket_mode are only valid for transport %q", v1alpha1.PortTransportVhostUser)
		}
		// The wire value still distinguishes a CNI-created veth from the
		// grout-tap handoff, even though both persist as veth.
		if s.requireGroutTap && port.GetTransport() != transportGroutTap {
			return "", status.Errorf(codes.InvalidArgument,
				"transport %q is not supported by this fast path: routed ports must use transport %q, "+
					"because the CRA cannot adopt a netdev created outside it",
				port.GetTransport(), transportGroutTap)
		}
	case v1alpha1.PortTransportVhostUser:
		if err := validateSocketPath(port.GetSocketPath()); err != nil {
			return "", status.Errorf(codes.InvalidArgument, "socket_path: %v", err)
		}
		if m := port.GetSocketMode(); m != v1alpha1.SocketModeClient && m != v1alpha1.SocketModeServer {
			return "", status.Errorf(codes.InvalidArgument,
				"invalid socket_mode %q (want %q or %q)", m, v1alpha1.SocketModeClient, v1alpha1.SocketModeServer)
		}
	default:
		return "", status.Errorf(codes.InvalidArgument,
			"invalid transport %q (want %q, %q or %q)",
			port.GetTransport(), v1alpha1.PortTransportVeth, transportGroutTap, v1alpha1.PortTransportVhostUser)
	}

	return transport, nil
}

// transportGroutTap is the wire-only transport the grout flavor's CNI sends. It
// is not an API enum: on the CRA side a grout tap is wired exactly like a veth
// port (routed interface, or enslaved to the L2VNI bridge), the only difference
// being which side creates the netdev. It is therefore persisted as veth.
const transportGroutTap = "grouttap"

// portTransportFromPB maps the wire transport string to the API enum, defaulting
// to veth for the empty and grout-tap values. An unrecognised value is returned
// verbatim so validateWiring can reject it.
func portTransportFromPB(transport string) v1alpha1.PortTransport {
	switch transport {
	case "", transportGroutTap:
		return v1alpha1.PortTransportVeth
	default:
		return v1alpha1.PortTransport(transport)
	}
}

// layer2AttachmentRefFromPB converts the wire l2a ref to the API type, or nil.
// The wire carries a bare name; the namespace the agent resolves
// Layer2Attachments in is stamped on here so the recorded reference is fully
// qualified and can be matched against the NodeNetworkConfig's stamped refs.
func (s *Server) layer2AttachmentRefFromPB(ref *pb.Layer2AttachmentRef) *v1alpha1.Layer2AttachmentRef {
	if ref == nil {
		return nil
	}
	return &v1alpha1.Layer2AttachmentRef{
		Name:      ref.GetName(),
		Namespace: s.l2Namespace,
	}
}

// layer2AttachmentRefLog renders an l2a ref for structured logging.
func layer2AttachmentRefLog(ref *v1alpha1.Layer2AttachmentRef) string {
	if ref == nil {
		return ""
	}
	return ref.Namespace + "/" + ref.Name
}

// layer2TrunkLog renders the trunk members for structured logging as
// "<namespace>/<name>@<vlan>", with "@auto" for a member that inherits the L2
// domain's own VLAN id.
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

// validateSocketPath rejects a vhost-user socket path that is not a plain
// absolute path.
//
// This is a trust boundary, not tidiness. The path arrives from the CNI, which
// takes it from a NetworkAttachmentDefinition, and it is persisted verbatim
// into NodeWorkloadPorts -- from where the grout CRA interpolates it into a
// grcli command line that is split on whitespace and executed line by line as
// root in the CRA netns. A path carrying a space or a newline would therefore
// stop being one argument and start being extra arguments or extra commands,
// which turns "may create a NAD" into "may reprogram the node's datapath".
// Other fast paths hand the value to a config renderer with its own quoting
// rules, so the check lives here, at the one gate every fast path passes
// through, rather than in one renderer.
func validateSocketPath(path string) error {
	if path == "" {
		return fmt.Errorf("required for transport %q", v1alpha1.PortTransportVhostUser)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%q is not an absolute path", path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%q is not a clean path", path)
	}
	if strings.ContainsFunc(path, func(r rune) bool {
		// Commas separate DPDK devargs, so they cannot appear in a value either.
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == ','
	}) {
		return fmt.Errorf("%q contains whitespace, a control character or a comma", path)
	}
	return nil
}
