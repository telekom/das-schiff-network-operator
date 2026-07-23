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
	"fmt"
	"time"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/ipam"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

// The grout flavor cannot adopt a moved-in kernel veth (grout's edge image has
// no af_packet/af_xdp/memif PMD), so the CRA-side of a routed pod attach is a
// grout-created net_tap rather than a veth peer. The datapath is therefore
// inverted relative to the veth transport:
//
//   1. The CNI hands the attachment to the node-local agent (notifyAgentAdd),
//      which persists it to NodeRoutedPorts and triggers the grout reconcile.
//      The grout-cra sidecar runs grcli, and grout creates a net_tap named
//      iface=<portName> inside the CRA netns. The agent returns that tap name.
//   2. The CNI polls the CRA netns until that tap netdev appears (the reconcile
//      is asynchronous), then moves it into the pod netns, renames it to the
//      requested interface name, addresses it from IPAM, and installs the
//      on-link default routes (unless L2). This makes the attach synchronous
//      from the pod/KubeVirt point of view: eth0 exists before ADD returns.
//
// grout keeps the tap's DPDK fd bound after the netdev is moved out of the CRA
// netns, so forwarding survives the move (validated on GCP, PoC Phase 5).

// groutTapPollTimeout bounds how long the CNI waits for grout to create the
// net_tap in the CRA netns after the agent has recorded the attachment. It must
// comfortably exceed a reconcile round-trip (NodeRoutedPorts write → watch →
// grcli apply) while staying within the runtime's CNI ADD deadline.
const groutTapPollTimeout = 60 * time.Second

// groutTapPollInterval is the poll cadence while waiting for the tap to appear.
const groutTapPollInterval = 200 * time.Millisecond

// cmdAddGroutTap implements the CNI ADD command for the grout net_tap transport.
func cmdAddGroutTap(conf *NetConf, args *skel.CmdArgs) error {
	craNetnsPath, err := resolveCRANetnsPath(conf)
	if err != nil {
		return err
	}

	// Delegate address allocation to the configured IPAM plugin.
	ipamResult, err := runIPAM(conf, args)
	if err != nil {
		return err
	}
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

	// Ask the agent to create the grout net_tap in the CRA netns. It returns the
	// tap netdev name to wait for (deterministic portName, echoed back).
	tapName, err := notifyAgentAdd(conf, args, portName(args.ContainerID), gwV4, gwV6, result)
	if err != nil {
		return err
	}
	if tapName == "" {
		tapName = portName(args.ContainerID)
	}
	// From here on, drop the agent-side attachment on any failure.
	defer func() {
		if !success {
			_ = notifyAgentDel(conf, args, portName(args.ContainerID))
		}
	}()

	// Wait for grout to create the tap in the CRA netns, then move it into the
	// pod netns, rename it, address it, and install on-link defaults.
	podIface, err := adoptGroutTap(conf, args, craNetnsPath, tapName, result)
	if err != nil {
		return err
	}

	result.Interfaces = []*current.Interface{podIface}
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

// cmdDelGroutTap implements the CNI DEL command for the grout net_tap transport.
func cmdDelGroutTap(conf *NetConf, args *skel.CmdArgs) error {
	// Release the IPAM allocation (best effort, but report a hard failure).
	if err := ipam.ExecDel(ipamTypeOrEmpty(conf), args.StdinData); err != nil {
		return fmt.Errorf("failed to release IPAM allocation: %w", err)
	}

	// Tell the agent to drop the attachment; the grout reconcile removes the
	// net_tap from the CRA fast path (which also destroys the netdev).
	_ = notifyAgentDel(conf, args, portName(args.ContainerID))

	// Remove the pod-side netdev if it still exists (idempotent).
	if args.Netns != "" {
		_ = teardownPodSide(args.Netns, args.IfName)
	}

	return nil
}

// adoptGroutTap waits for the grout-created net_tap named tapName to appear in
// the CRA netns, moves it into the pod netns as args.IfName, configures the
// allocated addresses, and installs on-link default routes (unless L2). It
// returns the pod-side interface descriptor.
func adoptGroutTap(conf *NetConf, args *skel.CmdArgs, craNetnsPath, tapName string, result *current.Result) (*current.Interface, error) {
	podNS, err := ns.GetNS(args.Netns)
	if err != nil {
		return nil, fmt.Errorf("failed to open pod netns %q: %w", args.Netns, err)
	}
	defer podNS.Close()

	// Poll the CRA netns for the tap and move it into the pod netns once it
	// exists. The move must happen while executing in the CRA netns.
	if werr := waitAndMoveGroutTap(craNetnsPath, tapName, int(podNS.Fd())); werr != nil {
		return nil, werr
	}

	iface := &current.Interface{Name: args.IfName, Sandbox: args.Netns}
	err = ns.WithNetNSPath(args.Netns, func(_ ns.NetNS) error {
		link, lerr := netlink.LinkByName(tapName)
		if lerr != nil {
			return fmt.Errorf("moved grout tap %q not found in pod netns: %w", tapName, lerr)
		}

		// Rename the tap to the requested interface name (must be down to rename).
		if derr := netlink.LinkSetDown(link); derr != nil {
			return fmt.Errorf("failed to set grout tap down for rename: %w", derr)
		}
		if rerr := netlink.LinkSetName(link, args.IfName); rerr != nil {
			return fmt.Errorf("failed to rename grout tap to %q: %w", args.IfName, rerr)
		}
		link, lerr = netlink.LinkByName(args.IfName)
		if lerr != nil {
			return fmt.Errorf("failed to look up renamed grout tap %q: %w", args.IfName, lerr)
		}
		iface.Mac = link.Attrs().HardwareAddr.String()

		// Assign the allocated addresses. KubeVirt's bridge binding relays these
		// to the guest; the guest may also set them statically via cloud-init.
		for _, ipc := range result.IPs {
			addr := &netlink.Addr{IPNet: &ipc.Address}
			if aerr := netlink.AddrAdd(link, addr); aerr != nil && !isExists(aerr) {
				return fmt.Errorf("failed to add address %s to grout tap: %w", ipc.Address.String(), aerr)
			}
		}

		if uerr := netlink.LinkSetUp(link); uerr != nil {
			return fmt.Errorf("failed to set grout tap up: %w", uerr)
		}

		// Routed mode needs an on-link default via the CRA link-local gateway so
		// KubeVirt bridge binding can relay the gateway to the guest. L2 mode
		// reaches its gateway over the shared L2 domain (no on-link default).
		if !conf.isL2() {
			if rerr := installOnLinkDefaults(conf, link, result); rerr != nil {
				return rerr
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("configuring grout tap in pod netns: %w", err)
	}
	return iface, nil
}

// waitAndMoveGroutTap polls the CRA netns until the tap netdev named tapName
// appears (grout's reconcile is asynchronous), then moves it into the pod netns
// identified by podNSFd. It returns an error if the tap does not appear within
// groutTapPollTimeout.
func waitAndMoveGroutTap(craNetnsPath, tapName string, podNSFd int) error {
	deadline := time.Now().Add(groutTapPollTimeout)
	for {
		moved, err := tryMoveGroutTap(craNetnsPath, tapName, podNSFd)
		if err != nil {
			return err
		}
		if moved {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for grout to create tap %q in CRA netns %q", groutTapPollTimeout, tapName, craNetnsPath)
		}
		time.Sleep(groutTapPollInterval)
	}
}

// tryMoveGroutTap performs one poll iteration inside the CRA netns: if the tap
// exists it is moved into the pod netns and (true, nil) is returned; if it does
// not yet exist (false, nil) is returned so the caller retries.
func tryMoveGroutTap(craNetnsPath, tapName string, podNSFd int) (bool, error) {
	moved := false
	err := ns.WithNetNSPath(craNetnsPath, func(_ ns.NetNS) error {
		link, lerr := netlink.LinkByName(tapName)
		if lerr != nil {
			// Not created yet; caller retries.
			return nil //nolint:nilerr // tap not present yet
		}
		if merr := netlink.LinkSetNsFd(link, podNSFd); merr != nil {
			return fmt.Errorf("failed to move grout tap %q into pod netns: %w", tapName, merr)
		}
		moved = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("polling CRA netns for grout tap: %w", err)
	}
	return moved, nil
}
