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

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/telekom/das-schiff-network-operator/pkg/routedcni/pb"
)

// dial connects to the node-local routed-cni gRPC socket. An empty socketPath
// uses DefaultSocketPath.
func dial(socketPath string) (*grpc.ClientConn, error) {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dial routed-cni socket %s: %w", socketPath, err)
	}
	return conn, nil
}

// Add calls the node-local agent to record a routed attachment (CNI ADD).
func Add(ctx context.Context, socketPath string, req *pb.AddRequest) error {
	conn, err := dial(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := pb.NewRoutedCNIClient(conn).Add(ctx, req); err != nil {
		return fmt.Errorf("routed-cni Add: %w", err)
	}
	return nil
}

// Del calls the node-local agent to remove a routed attachment (CNI DEL).
func Del(ctx context.Context, socketPath string, req *pb.DelRequest) error {
	conn, err := dial(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := pb.NewRoutedCNIClient(conn).Del(ctx, req); err != nil {
		return fmt.Errorf("routed-cni Del: %w", err)
	}
	return nil
}
