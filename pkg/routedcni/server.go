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

package routedcni

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/routedcni/pb"
)

// socketDirPerm is the permission for the directory holding the unix socket.
const socketDirPerm = 0o755

// Server is the node-local gRPC service the routed CNI plugin calls on ADD/DEL.
// It persists attachments into the node's NodeRoutedPorts object (the durable
// source of truth); the CRA agent reconciles that object into the datapath.
type Server struct {
	pb.UnimplementedRoutedCNIServer
	client   client.Client
	nodeName string
	log      logr.Logger
}

// NewServer builds a routed-cni gRPC server for the given node.
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
	// Remove a stale socket left by a previous run so Listen can bind.
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale socket %s: %w", socketPath, err)
	}

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}

	grpcSrv := grpc.NewServer()
	pb.RegisterRoutedCNIServer(grpcSrv, s)

	go func() {
		<-ctx.Done()
		grpcSrv.GracefulStop()
	}()

	s.log.Info("routed-cni gRPC server listening", "socket", socketPath, "node", s.nodeName)
	if err := grpcSrv.Serve(lis); err != nil {
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

// Add records (upserts) a routed attachment.
func (s *Server) Add(ctx context.Context, req *pb.AddRequest) (*pb.AddResponse, error) {
	port := req.GetPort()
	if port == nil || port.GetInterface() == "" {
		return nil, fmt.Errorf("interface is required")
	}
	if req.GetContainerId() == "" {
		return nil, fmt.Errorf("container_id is required")
	}

	entry := v1alpha1.RoutedPortEntry{
		PodNamespace: req.GetPodNamespace(),
		PodName:      req.GetPodName(),
		ContainerID:  req.GetContainerId(),
		VRF:          req.GetVrf(),
		RoutedPort: v1alpha1.RoutedPort{
			Interface:  port.GetInterface(),
			GatewayV4:  port.GetGatewayV4(),
			GatewayV6:  port.GetGatewayV6(),
			HostRoutes: port.GetHostRoutes(),
		},
	}

	if err := s.mutate(ctx, func(spec *v1alpha1.NodeRoutedPortsSpec) bool {
		UpsertEntry(spec, &entry)
		return true
	}); err != nil {
		return nil, fmt.Errorf("recording routed port: %w", err)
	}
	s.log.Info("recorded routed port", "container", entry.ContainerID, "interface", entry.Interface, "vrf", entry.VRF)
	return &pb.AddResponse{}, nil
}

// Del removes a routed attachment (idempotent).
func (s *Server) Del(ctx context.Context, req *pb.DelRequest) (*pb.DelResponse, error) {
	if req.GetContainerId() == "" {
		return nil, fmt.Errorf("container_id is required")
	}
	if err := s.mutate(ctx, func(spec *v1alpha1.NodeRoutedPortsSpec) bool {
		return RemoveEntry(spec, req.GetContainerId(), req.GetInterface())
	}); err != nil {
		return nil, fmt.Errorf("removing routed port: %w", err)
	}
	s.log.Info("removed routed port", "container", req.GetContainerId(), "interface", req.GetInterface())
	return &pb.DelResponse{}, nil
}

// mutate get-or-creates the node's NodeRoutedPorts object and applies fn to its
// spec under conflict retry. fn returns whether it changed the spec; if not, no
// write is issued.
func (s *Server) mutate(ctx context.Context, fn func(*v1alpha1.NodeRoutedPortsSpec) bool) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		nrp := &v1alpha1.NodeRoutedPorts{}
		err := s.client.Get(ctx, types.NamespacedName{Name: s.nodeName}, nrp)
		if apierrors.IsNotFound(err) {
			fresh := &v1alpha1.NodeRoutedPorts{}
			fresh.Name = s.nodeName
			if !fn(&fresh.Spec) {
				return nil
			}
			if cerr := s.client.Create(ctx, fresh); cerr != nil {
				return fmt.Errorf("creating NodeRoutedPorts: %w", cerr)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("getting NodeRoutedPorts: %w", err)
		}
		if !fn(&nrp.Spec) {
			return nil
		}
		if uerr := s.client.Update(ctx, nrp); uerr != nil {
			return fmt.Errorf("updating NodeRoutedPorts: %w", uerr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("mutating NodeRoutedPorts %q: %w", s.nodeName, err)
	}
	return nil
}
