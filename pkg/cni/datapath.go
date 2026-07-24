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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"

	"github.com/containernetworking/cni/pkg/skel"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

// portNamePrefix prefixes the CRA-side port name. Kept short so the full name
// stays within the 15-character interface-name limit.
const portNamePrefix = "cra"

// onLinkRouteMetric keeps the routed on-link default at a lower priority than the
// pod's own primary default (on eth0) so the virt-launcher pod itself is
// unaffected while the guest still learns the CRA gateway as its next hop.
const onLinkRouteMetric = 4096

// portName derives a deterministic, unique CRA-side port name from the CNI
// container ID. The name is bounded to 15 characters (kernel IFNAMSIZ-1).
func portName(containerID string) string {
	sum := sha256.Sum256([]byte(containerID))
	return portNamePrefix + hex.EncodeToString(sum[:])[:12]
}

// setupPodSide creates the veth pair inside the pod netns, configures the
// pod-side end with the allocated addresses, and moves the CRA-side peer
// (named portName) into the CRA network namespace. It returns the pod-side
// interface descriptor.
func setupPodSide(conf *NetConf, args *skel.CmdArgs, craNetnsPath, portName string, result *current.Result) (*current.Interface, error) {
	craNS, err := ns.GetNS(craNetnsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CRA netns %q: %w", craNetnsPath, err)
	}
	defer craNS.Close()

	iface := &current.Interface{Name: args.IfName, Sandbox: args.Netns}

	err = ns.WithNetNSPath(args.Netns, func(_ ns.NetNS) error {
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{
				Name: args.IfName,
				MTU:  conf.mtu(),
			},
			PeerName: portName,
		}
		if aerr := netlink.LinkAdd(veth); aerr != nil {
			return fmt.Errorf("failed to create veth pair: %w", aerr)
		}

		podLink, lerr := netlink.LinkByName(args.IfName)
		if lerr != nil {
			return fmt.Errorf("failed to look up pod-side veth %q: %w", args.IfName, lerr)
		}
		iface.Mac = podLink.Attrs().HardwareAddr.String()

		// Assign the allocated addresses to the pod-side interface. KubeVirt's
		// bridge binding relays these to the guest; the guest may also set them
		// statically via cloud-init.
		for _, ipc := range result.IPs {
			addr := &netlink.Addr{IPNet: &ipc.Address}
			if aerr := netlink.AddrAdd(podLink, addr); aerr != nil && !isExists(aerr) {
				return fmt.Errorf("failed to add address %s to pod interface: %w", ipc.Address.String(), aerr)
			}
		}

		if uerr := netlink.LinkSetUp(podLink); uerr != nil {
			return fmt.Errorf("failed to set pod interface up: %w", uerr)
		}

		// KubeVirt bridge binding derives the guest gateway from a route on the
		// pod interface (filterIPv4RoutesByInterface): it needs at least one
		// route whose next-hop interface is this link and relays that next-hop
		// to the guest as its gateway. Install on-link default routes via the
		// CRA link-local gateways.
		if rerr := installOnLinkDefaults(conf, podLink, result); rerr != nil {
			return rerr
		}

		// Move the peer end into the CRA network namespace.
		peerLink, perr := netlink.LinkByName(portName)
		if perr != nil {
			return fmt.Errorf("failed to look up CRA-side veth %q: %w", portName, perr)
		}
		if merr := netlink.LinkSetNsFd(peerLink, int(craNS.Fd())); merr != nil {
			return fmt.Errorf("failed to move CRA-side veth into CRA netns: %w", merr)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("configuring pod-side veth: %w", err)
	}
	return iface, nil
}

// installOnLinkDefaults adds on-link default routes via the CRA link-local
// gateways for whichever address families were allocated on the pod interface.
func installOnLinkDefaults(conf *NetConf, podLink netlink.Link, result *current.Result) error {
	var haveV4, haveV6 bool
	for _, ipc := range result.IPs {
		if ipc.Address.IP.To4() != nil {
			haveV4 = true
		} else {
			haveV6 = true
		}
	}
	addOnLinkDefault := func(gw net.IP) error {
		r := &netlink.Route{
			LinkIndex: podLink.Attrs().Index,
			Gw:        gw,
			Flags:     int(netlink.FLAG_ONLINK),
			Priority:  onLinkRouteMetric,
		}
		if rerr := netlink.RouteReplace(r); rerr != nil {
			return fmt.Errorf("failed to add on-link default route via %s: %w", gw, rerr)
		}
		return nil
	}
	if haveV4 {
		gw, gerr := conf.gatewayV4()
		if gerr != nil {
			return gerr
		}
		if rerr := addOnLinkDefault(gw); rerr != nil {
			return rerr
		}
	}
	if haveV6 {
		gw, gerr := conf.gatewayV6()
		if gerr != nil {
			return gerr
		}
		if rerr := addOnLinkDefault(gw); rerr != nil {
			return rerr
		}
	}
	return nil
}

// setupCRASide brings the moved CRA-side port up inside the CRA network
// namespace and returns its interface descriptor.
//
// The plugin is flavor-agnostic: it only wires the veth and brings the CRA-side
// port up. ALL L3 datapath programming (VRF binding, on-link gateway addresses,
// on-link host routes) is performed by the node-local CRA agent, which renders
// it its own way per flavor (netlink via frr-cra for FRR, NETCONF for VSR). The
// plugin hands the attachment to the agent over gRPC (see notifyAgentAdd).
func setupCRASide(craNetnsPath, portName string) (*current.Interface, error) {
	iface := &current.Interface{Name: portName, Sandbox: craNetnsPath}

	err := ns.WithNetNSPath(craNetnsPath, func(_ ns.NetNS) error {
		link, lerr := netlink.LinkByName(portName)
		if lerr != nil {
			return fmt.Errorf("failed to find moved CRA-side port %q: %w", portName, lerr)
		}
		iface.Mac = link.Attrs().HardwareAddr.String()

		if uerr := netlink.LinkSetUp(link); uerr != nil {
			return fmt.Errorf("failed to set CRA-side port up: %w", uerr)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bringing up CRA-side port: %w", err)
	}
	return iface, nil
}

// teardownPodSide removes the pod-side veth (which also deletes its moved peer).
func teardownPodSide(netnsPath, ifName string) error {
	if err := ns.WithNetNSPath(netnsPath, func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(ifName)
		if err != nil {
			return nil //nolint:nilerr // already gone
		}
		return netlink.LinkDel(link)
	}); err != nil {
		return fmt.Errorf("tearing down pod-side veth: %w", err)
	}
	return nil
}

// teardownCRASide removes the CRA-side port (and its on-link routes) from the
// CRA network namespace.
func teardownCRASide(craNetnsPath, portName string) error {
	if err := ns.WithNetNSPath(craNetnsPath, func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(portName)
		if err != nil {
			return nil //nolint:nilerr // already gone
		}
		return netlink.LinkDel(link)
	}); err != nil {
		return fmt.Errorf("tearing down CRA-side port: %w", err)
	}
	return nil
}

// isExists reports whether err indicates the object already exists.
func isExists(err error) bool {
	return err != nil && (err.Error() == "file exists" || err.Error() == "object exists")
}
