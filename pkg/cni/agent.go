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

	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni/pb"
)

// agentCallTimeout bounds the node-local gRPC call so a stuck agent cannot hang
// the CNI ADD/DEL past the runtime's own deadline.
const agentCallTimeout = 10 * time.Second

// notifyAgentAdd hands the attachment to the node-local CRA agent so it can
// render the CRA-side datapath (netlink via frr-cra for FRR, NETCONF for VSR).
// The plugin is flavor-agnostic; the agent decides how to program it. Routed and
// L2 attach modes differ in the request payload: routed carries VRF + on-link
// gateways + host routes, while L2 carries only the Layer2 reference(s) — a
// single untagged access ref or a tagged trunk list (the agent enslaves the port
// or its VLAN sub-interfaces to the matching bridges).
//
// att is non-nil only for the vhost-user transport, and carries the HOST-side
// socket path: that is the namespace the CRA agent and vSR run in.
func notifyAgentAdd(conf *NetConf, args *skel.CmdArgs, portName string, gwV4, gwV6 net.IP,
	result *current.Result, att *vhostUserAttachment,
) error {
	podNs, name := podIdentity(args.Args)
	port := &pb.WorkloadPort{
		Interface: portName,
		Transport: conf.transport(),
		//nolint:gosec // validateModes bounds mtu to MinPortMTU..MaxPortMTU
		Mtu: uint32(conf.mtu()),
	}
	if att != nil {
		port.SocketPath = att.HostPath
		port.SocketMode = att.Mode
	}
	req := &pb.AddRequest{
		PodNamespace: podNs,
		PodName:      name,
		ContainerId:  args.ContainerID,
		Port:         port,
	}

	if conf.isL2() {
		if conf.Layer2AttachmentRef != nil {
			req.Layer2AttachmentRef = &pb.Layer2AttachmentRef{Name: conf.Layer2AttachmentRef.Name}
		}
		for i := range conf.Layer2Trunk {
			member := &pb.Layer2TrunkMember{
				Ref: &pb.Layer2AttachmentRef{Name: conf.Layer2Trunk[i].Name},
			}
			// vlan 0 on the wire means "inherit the domain's own VLAN id"; the
			// plugin never resolves Layer2Attachments, so the agent does it.
			if vlan := conf.Layer2Trunk[i].VLAN; vlan != nil {
				member.Vlan = uint32(*vlan)
			}
			req.Layer2Trunk = append(req.Layer2Trunk, member)
		}
	} else {
		req.Vrf = conf.VRF
		port.GatewayV4 = gwV4.String() + "/32"
		port.GatewayV6 = gwV6.String() + "/128"
		port.HostRoutes = hostRoutes(result)
	}

	ctx, cancel := context.WithTimeout(context.Background(), agentCallTimeout)
	defer cancel()
	if err := workloadcni.Add(ctx, conf.AgentSocket, req); err != nil {
		return fmt.Errorf("notifying agent of attach add: %w", err)
	}
	return nil
}

// notifyAgentDel tells the node-local CRA agent to drop the routed attachment.
func notifyAgentDel(conf *NetConf, args *skel.CmdArgs, portName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), agentCallTimeout)
	defer cancel()
	if err := workloadcni.Del(ctx, conf.AgentSocket, &pb.DelRequest{
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
