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

// Package cni implements the "cni-routed" CNI plugin. It provides a fully
// routed, no-shared-L2 secondary attachment: the workload (a KubeVirt VM via
// the built-in bridge binding, or a routed pod later) gets a real routable
// IPv4 /32 + IPv6 /128, and the CRA-side veth end is moved into the CRA
// network namespace where the routing daemon (FRR / VSR) advertises on-link
// host routes to it via BGP.
package cni

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/containernetworking/cni/pkg/types"
)

const (
	// defaultLinkLocalV4 is the IPv4 link-local gateway address configured on
	// the CRA-side port and used by the workload as its on-link next-hop.
	defaultLinkLocalV4 = "169.254.1.1"
	// defaultLinkLocalV6 is the IPv6 link-local gateway address configured on
	// the CRA-side port and used by the workload as its on-link next-hop.
	defaultLinkLocalV6 = "fe80::1"
	// defaultMTU is used when the NetConf does not specify one.
	defaultMTU = 1500

	// AttachModeRouted is the default attach mode: the CRA-side port is routed
	// (VRF/underlay + on-link gateway + workload host routes). This is the
	// PR #343 behaviour.
	AttachModeRouted = "routed"
	// AttachModeL2 attaches the CRA-side port to an existing Layer2 bridge
	// (referenced by Layer2AttachmentRef) as a bridge slave, with no L3
	// addressing. The bridge/L2VNI is assumed to already exist on the node.
	AttachModeL2 = "l2"

	// TransportVeth is the default transport: a veth pair whose CRA-side end is
	// moved into the CRA network namespace.
	TransportVeth = "veth"
	// TransportVhostUser is a DPDK/virtio-user vhost-user socket transport,
	// rendered by VSR as an fpvhost fast-path virtual-port. It is VSR-only; the
	// FRR agent rejects it.
	TransportVhostUser = "vhostuser"

	// SocketModeClient / SocketModeServer are the vhost-user socket modes from
	// the workload's perspective. VSR inverts them when rendering fpvhost.
	SocketModeClient = "client"
	SocketModeServer = "server"
)

// NetConf is the CNI configuration for the cni-routed plugin.
type NetConf struct {
	types.NetConf

	// VRF is the name of the VRF (in the CRA network namespace) the workload's
	// port is enslaved to. Leave empty (or "default"/"main") to keep the port
	// in the CRA netns default routing table so the on-link host routes are
	// advertised by the UNDERLAY fabric BGP session (rather than exported as an
	// EVPN type-5 route from a tenant L3VNI VRF). Only meaningful in the
	// "routed" attach mode.
	VRF string `json:"vrf,omitempty"`

	// AttachMode selects how the CRA-side port is attached:
	//   - "routed" (default): routed attachment (VRF/underlay + on-link gateway
	//     + workload host routes).
	//   - "l2": bridge-slave attachment to an existing Layer2 domain referenced
	//     by Layer2AttachmentRef; no L3 addressing.
	AttachMode string `json:"attachMode,omitempty"`

	// Transport selects the CRA-side wiring:
	//   - "veth" (default): a veth pair whose CRA-side end is moved into the CRA
	//     netns.
	//   - "vhostuser": a DPDK/virtio-user vhost-user socket (VSR-only, rendered
	//     as an fpvhost fast-path virtual-port).
	Transport string `json:"transport,omitempty"`

	// Layer2AttachmentRef identifies the Layer2Attachment whose bridge the port
	// is enslaved to in the "l2" attach mode. Required when AttachMode is "l2",
	// otherwise ignored.
	Layer2AttachmentRef *Layer2AttachmentRef `json:"layer2AttachmentRef,omitempty"`

	// SocketPath is the vhost-user unix socket path shared with the workload.
	// Required when Transport is "vhostuser".
	SocketPath string `json:"socketPath,omitempty"`

	// SocketMode is the vhost-user socket mode from the workload's perspective
	// ("client" or "server"). Required when Transport is "vhostuser". VSR
	// inverts it when rendering the fpvhost virtual-port.
	SocketMode string `json:"socketMode,omitempty"`

	// AgentSocket overrides the unix socket the plugin uses to reach the
	// node-local CRA agent (routedcni.DefaultSocketPath when empty). The plugin
	// only wires the veth and moves the CRA-side port into the CRA netns; the
	// agent programs the datapath (netlink for FRR, NETCONF for VSR), so the two
	// flavors share one flavor-agnostic plugin.
	AgentSocket string `json:"agentSocket,omitempty"`

	// CRANetns selects the CRA network namespace the CRA-side veth end is moved
	// into. Accepted values:
	//   - "" or "auto": auto-discover (see TrunkInterface / discovery.go)
	//   - "<name>":     a named netns under /var/run/netns/<name>
	//   - "/path":      an absolute netns path (e.g. /proc/<pid>/ns/net)
	CRANetns string `json:"craNetns,omitempty"`

	// TrunkInterface is the interface name that identifies the CRA network
	// namespace during auto-discovery (the netns that owns this interface).
	// Defaults to "hbn" when empty. Mirrors BaseConfig.TrunkInterfaceName.
	TrunkInterface string `json:"trunkInterface,omitempty"`

	// LinkLocalGateways overrides the default link-local gateway addresses that
	// the workload uses as its on-link next-hop.
	LinkLocalGateways LinkLocalGateways `json:"linkLocalGateways,omitempty"`

	// MTU applied to the veth pair (and relayed to the guest). Defaults to 1500.
	MTU int `json:"mtu,omitempty"`

	// IPAM is the delegated IPAM configuration (e.g. host-local).
	IPAM json.RawMessage `json:"ipam,omitempty"`

	// DeviceID is the device-plugin-allocated device identifier, set directly by
	// some runtimes (Multus also mirrors it into RuntimeConfig.DeviceID when the
	// "deviceID" capability is enabled). Only meaningful for vhost-user.
	DeviceID string `json:"deviceID,omitempty"`

	// RuntimeConfig carries per-invocation values injected by the runtime when
	// the matching capabilities are enabled in the NetworkAttachmentDefinition
	// (deviceID, CNIDeviceInfoFile). Only meaningful for vhost-user.
	RuntimeConfig RuntimeConfig `json:"runtimeConfig,omitempty"`

	// PrevResult is populated by the runtime when chaining.
	RawPrevResult map[string]interface{} `json:"prevResult,omitempty"`
}

// RuntimeConfig holds the runtime-injected capability values.
type RuntimeConfig struct {
	// DeviceID is the device-plugin-allocated device (from the "deviceID"
	// capability).
	DeviceID string `json:"deviceID,omitempty"`
	// CNIDeviceInfoFile is the path the plugin writes the device info JSON to
	// (from the "CNIDeviceInfoFile" capability), consumed downstream (e.g. the
	// KubeVirt vhost-user hook sidecar).
	CNIDeviceInfoFile string `json:"CNIDeviceInfoFile,omitempty"`
}

// LinkLocalGateways holds the on-link next-hop addresses for each family.
type LinkLocalGateways struct {
	IPv4 string `json:"ipv4,omitempty"`
	IPv6 string `json:"ipv6,omitempty"`
}

// Layer2AttachmentRef identifies a Layer2Attachment by namespaced name. The
// node-local agent binds the port to the NNC Layer2 whose stamped AttachmentRef
// matches (see the intent builder), so no VNI or VLAN id is needed here.
type Layer2AttachmentRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// attachMode returns the configured attach mode or the default ("routed").
func (c *NetConf) attachMode() string {
	if c.AttachMode == "" {
		return AttachModeRouted
	}
	return c.AttachMode
}

// transport returns the configured transport or the default ("veth").
func (c *NetConf) transport() string {
	if c.Transport == "" {
		return TransportVeth
	}
	return c.Transport
}

// isL2 reports whether the port is attached in L2 (bridge-slave) mode.
func (c *NetConf) isL2() bool {
	return c.attachMode() == AttachModeL2
}

// isVhostUser reports whether the CRA-side transport is vhost-user.
func (c *NetConf) isVhostUser() bool {
	return c.transport() == TransportVhostUser
}

// mtu returns the configured MTU or the default.
func (c *NetConf) mtu() int {
	if c.MTU > 0 {
		return c.MTU
	}
	return defaultMTU
}

// trunkInterface returns the configured trunk interface name or the default.
func (c *NetConf) trunkInterface() string {
	if c.TrunkInterface != "" {
		return c.TrunkInterface
	}
	return "hbn"
}

// gatewayV4 returns the parsed IPv4 link-local gateway or the default.
func (c *NetConf) gatewayV4() (net.IP, error) {
	s := c.LinkLocalGateways.IPv4
	if s == "" {
		s = defaultLinkLocalV4
	}
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("invalid IPv4 link-local gateway %q", s)
	}
	return ip, nil
}

// gatewayV6 returns the parsed IPv6 link-local gateway or the default.
func (c *NetConf) gatewayV6() (net.IP, error) {
	s := c.LinkLocalGateways.IPv6
	if s == "" {
		s = defaultLinkLocalV6
	}
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() != nil {
		return nil, fmt.Errorf("invalid IPv6 link-local gateway %q", s)
	}
	return ip, nil
}

// ipamType extracts the delegated IPAM plugin type from the raw IPAM block.
func (c *NetConf) ipamType() (string, error) {
	var ipamConf struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(c.IPAM, &ipamConf); err != nil {
		return "", fmt.Errorf("failed to parse ipam configuration: %w", err)
	}
	if ipamConf.Type == "" {
		return "", fmt.Errorf("ipam.type is required")
	}
	return ipamConf.Type, nil
}

// parseConfig decodes and validates the plugin's stdin configuration.
func parseConfig(stdin []byte) (*NetConf, error) {
	conf := &NetConf{}
	if err := json.Unmarshal(stdin, conf); err != nil {
		return nil, fmt.Errorf("failed to parse network configuration: %w", err)
	}
	// IPAM is required for the veth transport (the pod-side address is relayed
	// to the guest). vhost-user addressing may be guest-side, so IPAM is
	// optional there.
	if len(conf.IPAM) == 0 {
		if !conf.isVhostUser() {
			return nil, fmt.Errorf("%q is required", "ipam")
		}
	} else if _, err := conf.ipamType(); err != nil {
		return nil, err
	}
	if err := conf.validateModes(); err != nil {
		return nil, err
	}
	// The on-link gateways are only used in the routed attach mode; validate
	// them eagerly there so errors surface at ADD time.
	if !conf.isL2() {
		if _, err := conf.gatewayV4(); err != nil {
			return nil, err
		}
		if _, err := conf.gatewayV6(); err != nil {
			return nil, err
		}
	}
	return conf, nil
}

// validateModes checks the transport and attach-mode axes and their
// mode-specific required fields.
func (c *NetConf) validateModes() error {
	switch c.attachMode() {
	case AttachModeRouted, AttachModeL2:
	default:
		return fmt.Errorf("invalid attachMode %q (want %q or %q)", c.AttachMode, AttachModeRouted, AttachModeL2)
	}
	switch c.transport() {
	case TransportVeth, TransportVhostUser:
	default:
		return fmt.Errorf("invalid transport %q (want %q or %q)", c.Transport, TransportVeth, TransportVhostUser)
	}

	if c.isL2() {
		if c.Layer2AttachmentRef == nil || c.Layer2AttachmentRef.Name == "" {
			return fmt.Errorf("layer2AttachmentRef.name is required when attachMode is %q", AttachModeL2)
		}
		if c.VRF != "" {
			return fmt.Errorf("vrf must not be set when attachMode is %q (the port is bridged, not routed)", AttachModeL2)
		}
	}

	if c.isVhostUser() {
		if c.SocketPath == "" {
			return fmt.Errorf("socketPath is required when transport is %q", TransportVhostUser)
		}
		switch c.SocketMode {
		case SocketModeClient, SocketModeServer:
		default:
			return fmt.Errorf("socketMode must be %q or %q when transport is %q", SocketModeClient, SocketModeServer, TransportVhostUser)
		}
	}
	return nil
}
