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
)

// NetConf is the CNI configuration for the cni-routed plugin.
type NetConf struct {
	types.NetConf

	// VRF is the name of the VRF (in the CRA network namespace) the workload's
	// port is enslaved to. Leave empty (or "default"/"main") to keep the port
	// in the CRA netns default routing table so the on-link host routes are
	// advertised by the UNDERLAY fabric BGP session (rather than exported as an
	// EVPN type-5 route from a tenant L3VNI VRF).
	VRF string `json:"vrf,omitempty"`

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

	// PrevResult is populated by the runtime when chaining.
	RawPrevResult map[string]interface{} `json:"prevResult,omitempty"`
}

// LinkLocalGateways holds the on-link next-hop addresses for each family.
type LinkLocalGateways struct {
	IPv4 string `json:"ipv4,omitempty"`
	IPv6 string `json:"ipv6,omitempty"`
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
	if len(conf.IPAM) == 0 {
		return nil, fmt.Errorf("%q is required", "ipam")
	}
	if _, err := conf.ipamType(); err != nil {
		return nil, err
	}
	// Validate gateway addresses eagerly so errors surface at ADD time.
	if _, err := conf.gatewayV4(); err != nil {
		return nil, err
	}
	if _, err := conf.gatewayV6(); err != nil {
		return nil, err
	}
	return conf, nil
}
