//go:build linux

/*
Copyright 2024.

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
	"fmt"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ipam"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

// About is the plugin version string reported to the runtime.
const About = "cni-routed: routed no-shared-L2 secondary attachment for KubeVirt VMs and pods"

// CmdAdd implements the CNI ADD command.
func CmdAdd(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}

	craNetnsPath, err := resolveCRANetnsPath(conf)
	if err != nil {
		return err
	}

	// Delegate address allocation to the configured IPAM plugin.
	ipamResult, err := runIPAM(conf, args)
	if err != nil {
		return err
	}
	// From here on, release the IPAM allocation on any failure.
	success := false
	defer func() {
		if !success {
			_ = ipam.ExecDel(ipamTypeOrEmpty(conf), args.StdinData)
		}
	}()

	result, err := current.NewResultFromResult(ipamResult)
	if err != nil {
		return fmt.Errorf("failed to convert IPAM result: %w", err)
	}
	if len(result.IPs) == 0 {
		return fmt.Errorf("IPAM plugin returned no addresses")
	}

	gwV4, err := conf.gatewayV4()
	if err != nil {
		return err
	}
	gwV6, err := conf.gatewayV6()
	if err != nil {
		return err
	}

	portName := portName(args.ContainerID)

	// Create the veth pair in the pod netns; the peer is the CRA-side port.
	podIface, err := setupPodSide(conf, args, craNetnsPath, portName, result)
	if err != nil {
		return err
	}

	// Move the CRA-side end into the CRA netns and bring it up.
	craIface, err := setupCRASide(craNetnsPath, portName)
	if err != nil {
		_ = teardownPodSide(args.Netns, args.IfName)
		return err
	}

	// Hand the attachment to the node-local CRA agent over gRPC. The agent
	// programs the CRA-side datapath (netlink via frr-cra for FRR, NETCONF for
	// VSR); the plugin itself is flavor-agnostic.
	if err := notifyAgentAdd(conf, args, portName, gwV4, gwV6, result); err != nil {
		_ = teardownCRASide(craNetnsPath, portName)
		_ = teardownPodSide(args.Netns, args.IfName)
		return err
	}

	result.Interfaces = []*current.Interface{podIface, craIface}
	for i := range result.IPs {
		idx := 0
		result.IPs[i].Interface = &idx
	}

	success = true
	if err := types.PrintResult(result, conf.CNIVersion); err != nil {
		return fmt.Errorf("printing CNI result: %w", err)
	}
	return nil
}

// CmdDel implements the CNI DEL command.
func CmdDel(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}

	// Release the IPAM allocation (best effort, but report a hard failure).
	if err := ipam.ExecDel(ipamTypeOrEmpty(conf), args.StdinData); err != nil {
		return fmt.Errorf("failed to release IPAM allocation: %w", err)
	}

	// Tell the node-local agent to drop the attachment.
	_ = notifyAgentDel(conf, args, portName(args.ContainerID))

	// Remove the CRA-side port (this also removes its on-link routes). Best
	// effort: the netns or link may already be gone.
	if craNetnsPath, derr := resolveCRANetnsPath(conf); derr == nil {
		_ = teardownCRASide(craNetnsPath, portName(args.ContainerID))
	}

	// Remove the pod-side veth (idempotent; ignore missing netns/link).
	if args.Netns != "" {
		_ = teardownPodSide(args.Netns, args.IfName)
	}

	return nil
}

// CmdCheck implements the CNI CHECK command.
func CmdCheck(args *skel.CmdArgs) error {
	if _, err := parseConfig(args.StdinData); err != nil {
		return err
	}
	if args.Netns == "" {
		return nil
	}
	if err := ns.WithNetNSPath(args.Netns, func(_ ns.NetNS) error {
		if _, lerr := netlink.LinkByName(args.IfName); lerr != nil {
			return fmt.Errorf("pod interface %q missing: %w", args.IfName, lerr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("checking pod interface: %w", err)
	}
	return nil
}

// PluginMain is the CNI entrypoint wiring for the plugin.
func PluginMain() {
	skel.PluginMainFuncs(
		skel.CNIFuncs{
			Add:   CmdAdd,
			Del:   CmdDel,
			Check: CmdCheck,
		},
		version.All,
		About,
	)
}

// runIPAM invokes the delegated IPAM plugin's ADD.
func runIPAM(conf *NetConf, args *skel.CmdArgs) (types.Result, error) {
	res, err := ipam.ExecAdd(ipamTypeOrEmpty(conf), args.StdinData)
	if err != nil {
		return nil, fmt.Errorf("failed to run IPAM plugin: %w", err)
	}
	return res, nil
}

// ipamTypeOrEmpty returns the delegated IPAM plugin type, or "" if it cannot be
// determined (parseConfig already validated it, so this is defensive).
func ipamTypeOrEmpty(conf *NetConf) string {
	t, err := conf.ipamType()
	if err != nil {
		return ""
	}
	return t
}
