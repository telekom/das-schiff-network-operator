//go:build linux

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

package cni

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/containernetworking/cni/pkg/skel"
	current "github.com/containernetworking/cni/pkg/types/100"

	"github.com/telekom/das-schiff-network-operator/pkg/routedcni"
	"github.com/telekom/das-schiff-network-operator/pkg/routedcni/pb"
)

// agentCallTimeout bounds the node-local gRPC call so a stuck agent cannot hang
// the CNI ADD/DEL past the runtime's own deadline.
const agentCallTimeout = 10 * time.Second

// notifyAgentAdd hands the routed attachment to the node-local CRA agent so it
// can render the CRA-side datapath (netlink via frr-cra for FRR, NETCONF for
// VSR). The plugin is flavor-agnostic; the agent decides how to program it.
func notifyAgentAdd(conf *NetConf, args *skel.CmdArgs, portName string, gwV4, gwV6 net.IP, result *current.Result) error {
	ns, name := podIdentity(args.Args)
	req := &pb.AddRequest{
		PodNamespace: ns,
		PodName:      name,
		ContainerId:  args.ContainerID,
		Vrf:          conf.VRF,
		Port: &pb.RoutedPort{
			Interface:  portName,
			GatewayV4:  gwV4.String() + "/32",
			GatewayV6:  gwV6.String() + "/128",
			HostRoutes: hostRoutes(result),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentCallTimeout)
	defer cancel()
	if err := routedcni.Add(ctx, conf.AgentSocket, req); err != nil {
		return fmt.Errorf("notifying agent of routed add: %w", err)
	}
	return nil
}

// notifyAgentDel tells the node-local CRA agent to drop the routed attachment.
func notifyAgentDel(conf *NetConf, args *skel.CmdArgs, portName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), agentCallTimeout)
	defer cancel()
	if err := routedcni.Del(ctx, conf.AgentSocket, &pb.DelRequest{
		ContainerId: args.ContainerID,
		Interface:   portName,
	}); err != nil {
		return fmt.Errorf("notifying agent of routed del: %w", err)
	}
	return nil
}

// hostRoutes renders the workload's allocated addresses as host routes
// ("<ip>/32" or "<ip>/128").
func hostRoutes(result *current.Result) []string {
	routes := make([]string, 0, len(result.IPs))
	for _, ipc := range result.IPs {
		ip := ipc.Address.IP
		if ip.To4() != nil {
			routes = append(routes, ip.String()+"/32")
		} else {
			routes = append(routes, ip.String()+"/128")
		}
	}
	return routes
}

// podIdentity extracts the Kubernetes pod namespace and name from the CNI_ARGS
// string (e.g. "K8S_POD_NAMESPACE=ns;K8S_POD_NAME=name;...").
func podIdentity(cniArgs string) (namespace, name string) {
	for _, kv := range strings.Split(cniArgs, ";") {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "K8S_POD_NAMESPACE":
			namespace = value
		case "K8S_POD_NAME":
			name = value
		}
	}
	return namespace, name
}
