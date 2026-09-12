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
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/containernetworking/cni/pkg/skel"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
)

// portNamePrefix prefixes the CRA-side veth name.
const portNamePrefix = "cra"

const (
	// maxTrunkVLANNameSuffix is the largest VLAN sub-interface suffix a trunk
	// port can grow: <port>.<vlan> is a real device name as well.
	maxTrunkVLANNameSuffix = ".4094"
	// portNameHashLen is the number of hash characters appended after
	// portNamePrefix. The generated value is a real veth device name and must
	// fit the 15-character kernel IFNAMSIZ-1 limit together with the longest
	// VLAN suffix: len("cra") + 7 + len(".4094") = 15. The budget is reserved
	// for every attachment, not just trunks, so the name does not depend on the
	// attach mode: DEL derives it from the config it is handed, which may no
	// longer say "trunk" by the time the sandbox goes away. The VSR resolves
	// infra-<portName> through the veth's ifalias, so that reference does not
	// constrain the device name.
	portNameHashLen = 7
	// portNameBase encodes the hash in base36 to keep ~36 bits of entropy in
	// the 7 characters left over.
	portNameBase = 36
)

// onLinkRouteMetric keeps the routed on-link default at a lower priority than the
// pod's own primary default (on eth0) so the virt-launcher pod itself is
// unaffected while the guest still learns the CRA gateway as its next hop.
const onLinkRouteMetric = 4096

// portName derives a deterministic, unique CRA-side port name from the CNI
// container ID and the pod-side interface name. The interface name is part of
// the key because the runtime (Multus) reuses one container ID for every
// attachment of a pod, so hashing the container ID alone would collide between
// two routed networks on the same pod.
func portName(containerID, ifName string) string {
	sum := sha256.Sum256([]byte(containerID + "/" + ifName))
	// Reduce the first 64 hash bits modulo base^len so the encoding is exactly
	// portNameHashLen characters (zero-padded), uniformly distributed.
	space := uint64(1)
	for range portNameHashLen {
		space *= portNameBase
	}
	digits := strconv.FormatUint(binary.BigEndian.Uint64(sum[:8])%space, portNameBase)
	return portNamePrefix + strings.Repeat("0", portNameHashLen-len(digits)) + digits
}

// openCRANetns opens the resolved CRA netns and verifies that the namespace
// behind the handle owns the trunk peer. The path was verified when it was
// resolved, but a /proc/<pid>/ns/net path may since have been re-pointed at
// another namespace (pid reuse), so the check is pinned to the handle every
// CRA-side operation of one ADD or DEL then runs through. The caller closes it.
func openCRANetns(conf *NetConf, craNetnsPath string) (ns.NetNS, error) {
	craNS, err := ns.GetNS(craNetnsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CRA netns %q: %w", craNetnsPath, err)
	}
	if err := verifyCRANetnsHandle(netns.NsHandle(craNS.Fd()), craNetnsPath, conf.trunkInterface()); err != nil {
		craNS.Close()
		return nil, err
	}
	return craNS, nil
}

// setupPodSide creates the veth pair inside the pod netns, configures the
// pod-side end with the allocated addresses, and moves the CRA-side peer
// (named portName) into the CRA network namespace through the verified handle
// craNS. It returns the pod-side interface descriptor.
func setupPodSide(conf *NetConf, args *skel.CmdArgs, craNS ns.NetNS, portName string, result *current.Result) (*current.Interface, error) {
	iface := &current.Interface{Name: args.IfName, Sandbox: args.Netns}

	err := ns.WithNetNSPath(args.Netns, func(_ ns.NetNS) (err error) {
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

		// Once the pair exists, any later failure must not leave a half-configured
		// veth behind: deleting the pod-side end also removes its peer, wherever
		// that peer currently lives. A cleanup that fails for any reason other
		// than the link already being gone is reported alongside the cause, so a
		// retried ADD hitting EEXIST can be traced back.
		configured := false
		defer func() {
			if configured {
				return
			}
			if derr := netlink.LinkDel(veth); derr != nil && !isLinkNotFound(derr) {
				err = errors.Join(err, fmt.Errorf("failed to clean up veth %q: %w", args.IfName, derr))
			}
		}()

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

		// In routed mode, KubeVirt bridge binding derives the guest gateway from
		// a route on the pod interface (filterIPv4RoutesByInterface): it needs
		// at least one route whose next-hop interface is this link and relays
		// that next-hop to the guest as its gateway. Install on-link default
		// routes via the CRA link-local gateways. In L2 mode the guest reaches
		// its gateway over the shared L2 domain, so no on-link default is added.
		if conf.isL2() {
			if rerr := installIPAMRoutes(podLink, result); rerr != nil {
				return rerr
			}
		} else if rerr := installOnLinkDefaults(conf, podLink, result); rerr != nil {
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
		configured = true
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
	haveV4, haveV6 := addressFamilies(result)
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

// installIPAMRoutes applies the routes the delegated IPAM returned (e.g. the
// `routes` block of static/host-local) to the pod interface in L2 mode, where
// the plugin has no gateway of its own. A route without a gateway falls back to
// the IPAM gateway of the same address family (the CNI convention, as in
// ip.ConfigureIface) and is on-link when there is none either.
func installIPAMRoutes(podLink netlink.Link, result *current.Result) error {
	for i, r := range result.Routes {
		if r == nil {
			return fmt.Errorf("IPAM returned an empty route entry at index %d", i)
		}
		gw := r.GW
		if gw == nil {
			for _, ipc := range result.IPs {
				if ipc != nil && ipc.Gateway != nil && (ipc.Gateway.To4() != nil) == (r.Dst.IP.To4() != nil) {
					gw = ipc.Gateway
					break
				}
			}
		}
		dst := r.Dst
		route := &netlink.Route{
			LinkIndex: podLink.Attrs().Index,
			Dst:       &dst,
			Gw:        gw,
			Priority:  r.Priority,
			MTU:       r.MTU,
			AdvMSS:    r.AdvMSS,
		}
		if r.Table != nil {
			route.Table = *r.Table
		}
		switch {
		case r.Scope != nil:
			if *r.Scope < 0 || *r.Scope > int(netlink.SCOPE_NOWHERE) {
				return fmt.Errorf("IPAM route %s has an invalid scope %d", r.Dst.String(), *r.Scope)
			}
			route.Scope = netlink.Scope(*r.Scope) //nolint:gosec // range-checked above
		case gw == nil:
			route.Scope = netlink.SCOPE_LINK
		}
		if rerr := netlink.RouteReplace(route); rerr != nil {
			return fmt.Errorf("failed to add IPAM route %s via %v: %w", r.Dst.String(), gw, rerr)
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
func setupCRASide(craNS ns.NetNS, portName string) (*current.Interface, error) {
	iface := &current.Interface{Name: portName, Sandbox: craNS.Path()}

	err := craNS.Do(func(_ ns.NetNS) error {
		link, lerr := netlink.LinkByName(portName)
		if lerr != nil {
			return fmt.Errorf("failed to find moved CRA-side port %q: %w", portName, lerr)
		}
		iface.Mac = link.Attrs().HardwareAddr.String()

		// The VSR flavor references this moved interface as its infrastructure
		// port infra-<portName>, which 6WIND resolves to the kernel interface by
		// its ifalias (not its devname). Without a matching alias the VSR cannot
		// bind the port and reaps the veth, which also destroys the pod-side
		// peer. Setting the alias here lets the VSR adopt the port and enslave it
		// into the target VRF. The FRR flavor ignores the alias, so this is safe
		// for both. The prefix is shared via workloadcni so it stays in sync with
		// the VSR renderer (follow-up #356).
		if aerr := netlink.LinkSetAlias(link, workloadcni.InfraPortPrefix+portName); aerr != nil {
			return fmt.Errorf("failed to set CRA-side port alias: %w", aerr)
		}

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
// A pod netns or link that is already gone counts as done: the runtime tearing
// the sandbox down races with DEL, and destroying the netns destroys the pair.
func teardownPodSide(netnsPath, ifName string) error {
	if err := deleteLinkInNetns(netnsPath, ifName); err != nil {
		return fmt.Errorf("tearing down pod-side veth: %w", err)
	}
	return nil
}

// teardownCRASide removes the CRA-side port (and its on-link routes) from the
// CRA network namespace behind the verified handle craNS. A port that is
// already gone counts as done.
func teardownCRASide(craNS ns.NetNS, portName string) error {
	if err := craNS.Do(func(_ ns.NetNS) error { return deleteLink(portName) }); err != nil {
		return fmt.Errorf("tearing down CRA-side port in netns %q: %w", craNS.Path(), err)
	}
	return nil
}

// deleteLinkInNetns deletes the named link inside netnsPath. Only a missing
// netns or a missing link is treated as already done; any other failure
// (permission, transient netlink error) is returned so the DEL is retried
// instead of silently leaving the link behind.
func deleteLinkInNetns(netnsPath, name string) error {
	err := ns.WithNetNSPath(netnsPath, func(_ ns.NetNS) error { return deleteLink(name) })
	var notExist ns.NSPathNotExistErr
	if errors.As(err, &notExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("in netns %q: %w", netnsPath, err)
	}
	return nil
}

// deleteLink deletes the named link in the current netns; a missing link
// counts as done.
func deleteLink(name string) error {
	link, lerr := netlink.LinkByName(name)
	if lerr != nil {
		if isLinkNotFound(lerr) {
			return nil
		}
		return fmt.Errorf("failed to look up %q: %w", name, lerr)
	}
	if derr := netlink.LinkDel(link); derr != nil && !isLinkNotFound(derr) {
		return fmt.Errorf("failed to delete %q: %w", name, derr)
	}
	return nil
}

// isLinkNotFound reports whether err indicates the link does not exist.
func isLinkNotFound(err error) bool {
	var notFound netlink.LinkNotFoundError
	return errors.As(err, &notFound) || errors.Is(err, unix.ENODEV)
}

// isExists reports whether err indicates the object already exists. netlink
// surfaces this as a syscall.Errno, so match on the errno rather than on the
// (unstable) error text.
func isExists(err error) bool {
	return errors.Is(err, unix.EEXIST) || errors.Is(err, os.ErrExist)
}
