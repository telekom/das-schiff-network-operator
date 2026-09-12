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
	"net"

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
//
// The rollbacks armed along the way run in reverse order on failure, and they
// follow the same rule as DEL: the IPAM allocation is released only once the
// agent has confirmed the attachment is not (or no longer) recorded. A retained
// allocation is a leak; a released one whose entry lingers is a re-used address
// whose host routes still point at the stale port.
func CmdAdd(args *skel.CmdArgs) (retErr error) {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}

	craNetnsPath, err := resolveCRANetnsPath(conf)
	if err != nil {
		return err
	}

	// Delegate address allocation to the configured IPAM plugin. IPAM is
	// optional in the L2 attach mode, where the workload is addressed inside
	// the shared L2 domain rather than by this plugin.
	result, releaseIPAM, err := runOptionalIPAM(conf, args)
	if err != nil {
		return err
	}
	// From here on, release the IPAM allocation on any failure — unless the
	// compensating DEL below could not confirm that the agent dropped the
	// entry, in which case the allocation stays reserved for the retried DEL.
	success := false
	retainIPAM := false
	defer func() {
		if !success && !retainIPAM {
			releaseIPAM()
		}
	}()

	if err := prepareIPAMResult(conf, result); err != nil {
		return err
	}
	gwV4, gwV6, err := conf.gateways()
	if err != nil {
		return err
	}

	portName := portName(args.ContainerID, args.IfName)

	// One verified handle serves every CRA-side step of this ADD, so a netns
	// path re-pointed halfway through cannot redirect any of them.
	craNS, err := openCRANetns(conf, craNetnsPath)
	if err != nil {
		return err
	}
	defer craNS.Close()

	// Create the veth pair in the pod netns; the peer is the CRA-side port.
	podIface, err := setupPodSide(conf, args, craNS, portName, result)
	if err != nil {
		return err
	}
	// From here on, tear the datapath down again on any failure. If that
	// teardown cannot confirm the pair is gone, the CRA-side port may still
	// route the address, so the IPAM allocation is kept for the runtime's DEL
	// (which retries the removal) and the failure is surfaced.
	defer func() {
		if success {
			return
		}
		if terr := errors.Join(teardownCRASide(craNS, portName), teardownPodSide(args.Netns, args.IfName)); terr != nil {
			retainIPAM = true
			retErr = errors.Join(retErr, fmt.Errorf(
				"failed to tear the datapath down after a failed ADD (IPAM allocation retained for DEL): %w", terr))
		}
	}()

	// Move the CRA-side end into the CRA netns and bring it up.
	craIface, err := setupCRASide(craNS, portName)
	if err != nil {
		return err
	}

	// Hand the attachment to the node-local CRA agent over gRPC. The agent
	// programs the CRA-side datapath (netlink via frr-cra for FRR, NETCONF for
	// VSR); the plugin itself is flavor-agnostic. The compensating DEL is armed
	// before the call: the agent may have persisted the entry and lost the
	// connection before answering, and a failed ADD must not leave a durable
	// NodeWorkloadPorts entry for a port that is torn down right below. DEL is
	// idempotent, so compensating an ADD that never got through is harmless.
	// If the compensation itself fails the entry may linger, so the IPAM
	// allocation is kept (a re-used address would be routed toward the stale
	// port) and the failure is surfaced for the runtime's DEL to retry.
	defer func() {
		if success {
			return
		}
		if delErr := notifyAgentDel(conf, args, portName); delErr != nil {
			retainIPAM = true
			retErr = errors.Join(retErr, fmt.Errorf(
				"failed to compensate the attachment after a failed ADD (IPAM allocation retained for DEL): %w", delErr))
		}
	}()
	if err := notifyAgentAdd(conf, args, portName, gwV4, gwV6, result); err != nil {
		return err
	}

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

	portName := portName(args.ContainerID, args.IfName)
	var errs []error

	// Tell the node-local agent to drop the attachment. The veth below vanishes
	// regardless, but a stale NodeWorkloadPorts entry has nothing else to reclaim
	// it (its VRF placement and FRR export permit linger), so surface the error
	// and let the runtime retry the DEL.
	agentErr := notifyAgentDel(conf, args, portName)
	if agentErr != nil {
		errs = append(errs, agentErr)
	}

	// Remove the CRA-side port (this also removes its on-link routes). The
	// netns or link may already be gone, which counts as done; a netns that is
	// there but turns out not to be the CRA's (re-pointed path) cannot hold
	// the port, so nothing is removed from it, and the DEL is retried.
	craNetnsPath, resolveErr := resolveCRANetnsPath(conf)
	if resolveErr == nil {
		if craNS, oerr := openCRANetns(conf, craNetnsPath); oerr == nil {
			if err := teardownCRASide(craNS, portName); err != nil {
				errs = append(errs, err)
			}
			craNS.Close()
		} else if !errors.As(oerr, new(ns.NSPathNotExistErr)) {
			errs = append(errs, oerr)
		}
	}

	// Remove the pod-side veth, which destroys the pair wherever the peer lives.
	// A DEL without a pod netns means the runtime already destroyed the sandbox,
	// and the pair with it.
	var podSideErr error
	if args.Netns != "" {
		if podSideErr = teardownPodSide(args.Netns, args.IfName); podSideErr != nil {
			errs = append(errs, podSideErr)
		}
	}

	// An unresolvable CRA netns is harmless as long as the pod-side teardown
	// took the pair down; if that did not succeed either, the port may still
	// exist and the DEL has to be retried.
	if resolveErr != nil && podSideErr != nil {
		errs = append(errs, fmt.Errorf("CRA netns could not be resolved to remove port %q: %w", portName, resolveErr))
	}

	// Release the IPAM allocation last, and only once everything above
	// succeeded: while the durable entry lingers its host routes are still
	// exported, and while the CRA-side veth lingers the kernel still routes the
	// address toward the stale port — either way an address handed straight to
	// another workload would be black-holed or hijacked. A failed step returns
	// an error, the runtime retries the DEL, and the retry releases it. An L2
	// attachment without IPAM has nothing to release.
	if len(errs) == 0 && len(conf.IPAM) != 0 {
		if err := ipam.ExecDel(ipamTypeOrEmpty(conf), args.StdinData); err != nil {
			errs = append(errs, fmt.Errorf("failed to release IPAM allocation: %w", err))
		}
	}

	return errors.Join(errs...)
}

// prepareIPAMResult checks the delegated IPAM result against the attach mode
// and shapes it for the pod interface.
//
// A routed attachment reaches everything, including its own IPAM pool, through
// the on-link CRA gateway. An address kept at the pool's prefix length
// (host-local hands out e.g. a /24) would add a connected route on the pod side
// and make same-pool traffic ARP on the isolated veth instead, so the addresses
// are narrowed to host prefixes — which is also what the agent exports. An L2
// attachment lives in the pool's subnet and keeps it; its IPAM routes are
// applied to the pod interface, which needs an address to reach them from.
func prepareIPAMResult(conf *NetConf, result *current.Result) error {
	if conf.isL2() {
		if len(result.Routes) > 0 && len(result.IPs) == 0 {
			return fmt.Errorf("IPAM returned routes but no addresses to reach them from")
		}
		return nil
	}
	if len(result.IPs) == 0 {
		return fmt.Errorf("IPAM plugin returned no addresses")
	}
	normalizeHostPrefixes(result)
	return nil
}

// normalizeHostPrefixes narrows every address of the IPAM result to a host
// prefix (/32, /128) in place, keeping the address itself.
func normalizeHostPrefixes(result *current.Result) {
	const v4Bits, v6Bits = 32, 128
	for i := range result.IPs {
		bits := v6Bits
		if result.IPs[i].Address.IP.To4() != nil {
			bits = v4Bits
		}
		result.IPs[i].Address.Mask = net.CIDRMask(bits, bits)
	}
}

// CmdCheck implements the CNI CHECK command.
func CmdCheck(args *skel.CmdArgs) error {
	_, err := parseConfig(args.StdinData)
	if err != nil {
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

// runOptionalIPAM runs the delegated IPAM plugin if one is configured,
// returning the CNI result and a cleanup function that releases the allocation.
// L2 attachments can run without IPAM, so a missing IPAM block yields an empty
// result and a no-op cleanup.
func runOptionalIPAM(conf *NetConf, args *skel.CmdArgs) (*current.Result, func(), error) {
	if len(conf.IPAM) == 0 {
		return &current.Result{}, func() {}, nil
	}
	ipamResult, err := runIPAM(conf, args)
	if err != nil {
		return nil, nil, err
	}
	// From here on the allocation exists and has to be released on any failure,
	// including the conversion below: CmdAdd only arms its cleanup once this
	// returns successfully.
	cleanup := func() { _ = ipam.ExecDel(ipamTypeOrEmpty(conf), args.StdinData) }
	result, err := current.NewResultFromResult(ipamResult)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to convert IPAM result: %w", err)
	}
	return result, cleanup, nil
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
