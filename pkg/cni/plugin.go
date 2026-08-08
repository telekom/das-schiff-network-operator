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
	"errors"
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
const About = "cni-workload: routed no-shared-L2 secondary attachment for KubeVirt VMs and pods"

// CmdAdd implements the CNI ADD command.
func CmdAdd(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}

	// vhost-user is a fast-path socket transport (VSR-only): no veth, no
	// CRA-side netns port move. It is handled entirely by the agent, so it does
	// not need the CRA netns resolved.
	if conf.isVhostUser() {
		return cmdAddVhostUser(conf, args)
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

	portName := portName(args.ContainerID, args.IfName)

	// Create the veth pair in the pod netns; the peer is the CRA-side port.
	podIface, err := setupPodSide(conf, args, craNetnsPath, portName, result)
	if err != nil {
		return err
	}
	// From here on, tear the datapath down again on any failure.
	defer func() {
		if !success {
			_ = teardownCRASide(craNetnsPath, portName)
			_ = teardownPodSide(args.Netns, args.IfName)
		}
	}()

	// Move the CRA-side end into the CRA netns and bring it up.
	craIface, err := setupCRASide(craNetnsPath, portName)
	if err != nil {
		return err
	}

	// Hand the attachment to the node-local CRA agent over gRPC. The agent
	// programs the CRA-side datapath (netlink via frr-cra for FRR, NETCONF for
	// VSR); the plugin itself is flavor-agnostic.
	if err := notifyAgentAdd(conf, args, portName, gwV4, gwV6, result, nil); err != nil {
		return err
	}
	defer func() {
		if !success {
			_ = notifyAgentDel(conf, args, portName)
		}
	}()

	result.Interfaces = []*current.Interface{podIface, craIface}
	for i := range result.IPs {
		idx := 0
		result.IPs[i].Interface = &idx
	}

	// Only commit once the result has been handed back to the runtime: if
	// printing fails the runtime never learns about the attachment, so the
	// deferred rollbacks above must still run.
	if err := types.PrintResult(result, conf.CNIVersion); err != nil {
		return fmt.Errorf("printing CNI result: %w", err)
	}
	success = true
	return nil
}

// CmdDel implements the CNI DEL command.
//
// DEL must make as much cleanup progress as possible even when one step fails,
// so every step runs and the failures are aggregated: the runtime retries DEL
// only if a non-nil error is returned, and a partially-torn-down attachment
// would otherwise leak interfaces, routes and NodeWorkloadPorts entries.
func CmdDel(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}

	if conf.isVhostUser() {
		return cmdDelVhostUser(conf, args)
	}

	portName := portName(args.ContainerID, args.IfName)
	var errs []error

	// Release the IPAM allocation.
	if err := ipam.ExecDel(ipamTypeOrEmpty(conf), args.StdinData); err != nil {
		errs = append(errs, fmt.Errorf("failed to release IPAM allocation: %w", err))
	}

	// Tell the node-local agent to drop the attachment. A stale NodeWorkloadPorts
	// entry keeps the agent programming the CRA-side datapath, so surface this.
	if err := notifyAgentDel(conf, args, portName); err != nil {
		errs = append(errs, err)
	}

	// Remove the CRA-side port (this also removes its on-link routes). Best
	// effort: the netns or link may already be gone.
	if craNetnsPath, derr := resolveCRANetnsPath(conf); derr == nil {
		if err := teardownCRASide(craNetnsPath, portName); err != nil {
			errs = append(errs, err)
		}
	}

	// Remove the pod-side veth (idempotent; ignore missing netns/link).
	if args.Netns != "" {
		if err := teardownPodSide(args.Netns, args.IfName); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// CmdCheck implements the CNI CHECK command.
func CmdCheck(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}
	// The vhost-user transport creates no pod-side netdev at all: the workload
	// talks to a unix socket owned by the device plugin. There is nothing to
	// look up in the pod netns, so checking for a link would always fail.
	if conf.isVhostUser() {
		return nil
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
