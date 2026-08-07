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

// Package cni implements the "cni-workload" CNI plugin. It provides a fully
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
	"strings"

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

// NetConf is the CNI configuration for the cni-workload plugin.
type NetConf struct {
	types.NetConf

	// VRF is the name of the VRF (in the CRA network namespace) the workload's
	// port is enslaved to. Leave empty (or "default"/"main") to keep the port
	// in the CRA netns default routing table so the on-link host routes are
	// advertised by the UNDERLAY fabric BGP session (rather than exported as an
	// EVPN type-5 route from a tenant L3VNI VRF).
	VRF string `json:"vrf,omitempty"`

	// NodeConfig carries the node-level trust anchors: the agent socket, the
	// CRA netns and the trunk interface that identifies it. They are read from
	// NodeConfigPath, never from the attachment config (parseConfig rejects
	// them there), because the attachment config is tenant-writable while
	// these decide where a root-run plugin wires the port. The plugin only
	// wires the veth and moves the CRA-side port into the CRA netns; the agent
	// programs the datapath (netlink for FRR, NETCONF for VSR), so the two
	// flavors share one flavor-agnostic plugin.
	NodeConfig `json:"-"`

	// LinkLocalGateways overrides the default link-local gateway addresses that
	// the workload uses as its on-link next-hop. Only link-local unicast
	// addresses (169.254.0.0/16, fe80::/10) are accepted: the gateway is
	// configured as an address on the CRA-side port, and the attachment config
	// must not be able to claim an underlay or node address there.
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
	return defaultTrunkInterface
}

// nodeOnlyKeys are the attachment-config keys that are refused because their
// values are node-level trust anchors (see NodeConfigPath). Refusing rather
// than ignoring them keeps a stale attachment config from silently changing
// meaning, and surfaces the misplaced setting to whoever wrote it.
var nodeOnlyKeys = []string{"agentSocket", "craNetns", "trunkInterface"}

// rejectNodeOnlyKeys fails when the attachment config carries a key that is
// only accepted from the node config.
func rejectNodeOnlyKeys(stdin []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdin, &raw); err != nil {
		return fmt.Errorf("failed to parse network configuration: %w", err)
	}
	for _, key := range nodeOnlyKeys {
		if _, present := raw[key]; present {
			return fmt.Errorf("%q is a node-level setting read from %s and is not accepted from the "+
				"network attachment configuration", key, NodeConfigPath)
		}
	}
	return nil
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
	if !ip.IsLinkLocalUnicast() {
		return nil, fmt.Errorf("IPv4 gateway %q is not link-local (169.254.0.0/16)", s)
	}
	return ip, nil
}

// gateways returns both on-link gateways of the attachment.
func (c *NetConf) gateways() (v4, v6 net.IP, err error) {
	if v4, err = c.gatewayV4(); err != nil {
		return nil, nil, err
	}
	if v6, err = c.gatewayV6(); err != nil {
		return nil, nil, err
	}
	return v4, v6, nil
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
	if !ip.IsLinkLocalUnicast() {
		return nil, fmt.Errorf("IPv6 gateway %q is not link-local (fe80::/10)", s)
	}
	return ip, nil
}

// allowedIPAMTypes are the IPAM plugins the attachment config may delegate to.
// The type names a binary on CNI_PATH that the plugin executes as root with
// the (NAD-supplied) config on stdin, so it is restricted to actual address
// managers: without the list a config could name any chained plugin —
// host-device, tuning, bridge — and have it reconfigure the host.
var allowedIPAMTypes = map[string]bool{
	"static":      true,
	"host-local":  true,
	"whereabouts": true,
}

// nodeOnlyIPAMKeys are the IPAM options that are refused from the attachment
// config because they point the (root-run) IPAM binary at host paths or
// credentials: the whole ipam block is handed to it verbatim on ADD and DEL,
// so restricting the binary name alone would still let a NAD author create or
// remove allocation state under an arbitrary host directory, read an
// arbitrary host file as resolv.conf, or use a foreign kubeconfig /
// datastore. Those settings belong to the node (the IPAM's compiled-in
// defaults or its node-level config file), not to the tenant. Keys are
// matched case-insensitively because encoding/json — which the IPAM plugins
// use to decode their config — accepts any casing of a field name.
var nodeOnlyIPAMKeys = map[string][]string{
	"host-local": {"dataDir", "resolvConf"},
	"whereabouts": {
		"configuration_path", "log_file", "kubernetes", "datastore",
		"etcd_host", "etcd_username", "etcd_password", "etcd_key_file", "etcd_cert_file", "etcd_ca_cert_file",
	},
}

// ipamType extracts and validates the delegated IPAM plugin type from the raw
// IPAM block. It is part of config parsing, so ADD, DEL and CHECK all refuse a
// block that names an unsupported plugin or carries node-only options before
// anything is delegated.
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
	if !allowedIPAMTypes[ipamConf.Type] {
		return "", fmt.Errorf("ipam.type %q is not a supported IPAM plugin (static, host-local, whereabouts)", ipamConf.Type)
	}
	// Scan the raw keys rather than decoding into a struct so that every
	// spelling the IPAM plugin would accept is seen.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(c.IPAM, &keys); err != nil {
		return "", fmt.Errorf("failed to parse ipam configuration: %w", err)
	}
	for present := range keys {
		for _, key := range nodeOnlyIPAMKeys[ipamConf.Type] {
			if strings.EqualFold(present, key) {
				return "", fmt.Errorf("ipam.%s is a node-level %s setting and is not accepted from the "+
					"network attachment configuration", present, ipamConf.Type)
			}
		}
	}
	return ipamConf.Type, nil
}

// parseConfig decodes and validates the plugin's stdin configuration.
func parseConfig(stdin []byte) (*NetConf, error) {
	conf := &NetConf{}
	if err := json.Unmarshal(stdin, conf); err != nil {
		return nil, fmt.Errorf("failed to parse network configuration: %w", err)
	}
	if err := rejectNodeOnlyKeys(stdin); err != nil {
		return nil, err
	}
	nodeCfg, err := loadNodeConfig(nodeConfigPath)
	if err != nil {
		return nil, err
	}
	conf.NodeConfig = *nodeCfg
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
