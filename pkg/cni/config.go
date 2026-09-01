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

	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
)

const (
	// defaultLinkLocalV4 is the IPv4 link-local gateway address configured on
	// the CRA-side port and used by the workload as its on-link next-hop.
	defaultLinkLocalV4 = "169.254.1.1"
	// defaultLinkLocalV6 is the IPv6 link-local gateway address configured on
	// the CRA-side port and used by the workload as its on-link next-hop.
	defaultLinkLocalV6 = "fe80::1"
	// defaultMTU is used when the NetConf does not specify one. It is shared with
	// the agent, which applies the same default to a request that carries none.
	defaultMTU = workloadcni.DefaultPortMTU
	// maxVLANID is the highest assignable 802.1Q VLAN id (4095 is reserved).
	maxVLANID = 4094

	// AttachModeRouted is the default attach mode: the CRA-side port is routed
	// (VRF/underlay + on-link gateway + workload host routes). This is the
	// PR #343 behaviour.
	AttachModeRouted = "routed"
	// AttachModeL2 attaches the CRA-side port to an existing Layer2 bridge
	// (referenced by Layer2AttachmentRef) as a bridge slave, with no L3
	// addressing. The bridge/L2VNI is assumed to already exist on the node.
	AttachModeL2 = "l2"
)

// NetConf is the CNI configuration for the cni-workload plugin.
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

	// Layer2AttachmentRef identifies the Layer2Attachment whose bridge the port
	// is enslaved to as an untagged access port in the "l2" attach mode.
	// Mutually exclusive with Layer2Trunk; exactly one of the two is required
	// when AttachMode is "l2", otherwise both are ignored.
	Layer2AttachmentRef *Layer2AttachmentRef `json:"layer2AttachmentRef,omitempty"`

	// Layer2Trunk carries several Layer2 domains on the port as an 802.1Q
	// trunk. Every member is tagged: the port itself is never an untagged
	// member, so untagged frames and frames with an unlisted VLAN id are not
	// forwarded. Mutually exclusive with Layer2AttachmentRef and only valid
	// when AttachMode is "l2".
	Layer2Trunk []Layer2TrunkMember `json:"layer2Trunk,omitempty"`

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

// Layer2AttachmentRef identifies a Layer2Attachment by name. The node agent
// resolves the name in the namespace it was configured with
// (--intent-namespace, the same namespace the operator's intent pipeline is
// scoped to) and binds the port to the NNC Layer2 whose stamped AttachmentRef
// matches (see the intent builder), so no namespace, VNI or VLAN id is needed
// here.
type Layer2AttachmentRef struct {
	Name string `json:"name"`
}

// Layer2TrunkMember is one tagged member of a trunk attachment.
type Layer2TrunkMember struct {
	Layer2AttachmentRef `json:",inline"`

	// VLAN is the workload-side 802.1Q id the domain is carried under. When
	// unset the domain's own VLAN id is used, which the node agent resolves
	// from the NodeNetworkConfig; setting it translates between the
	// workload-side id and the fabric-side id of the domain.
	VLAN *uint16 `json:"vlan,omitempty"`
}

// attachMode returns the configured attach mode or the default ("routed").
func (c *NetConf) attachMode() string {
	if c.AttachMode == "" {
		return AttachModeRouted
	}
	return c.AttachMode
}

// isL2 reports whether the port is attached in L2 (bridge-slave) mode.
func (c *NetConf) isL2() bool {
	return c.attachMode() == AttachModeL2
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

// gateways returns both on-link gateways of a routed attachment. They only
// exist in the routed attach mode; an L2 attachment reaches its gateway over
// the shared domain and must not be rejected because of an (unused) invalid
// gateway override, so none are returned for it.
func (c *NetConf) gateways() (v4, v6 net.IP, err error) {
	if c.isL2() {
		return nil, nil, nil
	}
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
	// IPAM is required for routed attachments (the pod-side address is relayed
	// to the guest). It is optional in L2 attach mode, where the workload is
	// addressed inside the shared L2 domain. When configured it is always
	// delegated and applied.
	if len(conf.IPAM) == 0 {
		if !conf.isL2() {
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

// validateModes checks the attach mode and its mode-specific required fields.
// validateMTU bounds the requested MTU so the value handed to the agent (and
// from there to the datapath) is always one an interface can be configured
// with. Whether the L2 domain can carry it is only knowable on the node, so the
// agent checks that when it merges the attachment.
func (c *NetConf) validateMTU() error {
	if c.MTU != 0 && (c.MTU < workloadcni.MinPortMTU || c.MTU > workloadcni.MaxPortMTU) {
		return fmt.Errorf("mtu %d is out of range (%d-%d)", c.MTU, workloadcni.MinPortMTU, workloadcni.MaxPortMTU)
	}
	return nil
}

func (c *NetConf) validateModes() error {
	if err := c.validateMTU(); err != nil {
		return err
	}
	switch c.attachMode() {
	case AttachModeRouted, AttachModeL2:
	default:
		return fmt.Errorf("invalid attachMode %q (want %q or %q)", c.AttachMode, AttachModeRouted, AttachModeL2)
	}
	if c.isL2() {
		if err := c.validateL2Attach(); err != nil {
			return err
		}
	} else if len(c.Layer2Trunk) > 0 || c.Layer2AttachmentRef != nil {
		return fmt.Errorf("layer2AttachmentRef and layer2Trunk are only valid when attachMode is %q", AttachModeL2)
	}

	return nil
}

// validateL2Attach checks the L2 attach mode's own fields: it is either an
// untagged access port on a single Layer2 domain, or an all-tagged trunk over
// several of them, never both. Mixing the two would leave the port an untagged
// bridge slave while tagged sub-interfaces demux the rest, so every VLAN id
// without a member would leak into the untagged domain — and VSR bridges have
// no VLAN filtering to guard against that.
func (c *NetConf) validateL2Attach() error {
	hasRef := c.Layer2AttachmentRef != nil
	hasTrunk := len(c.Layer2Trunk) > 0
	switch {
	case hasRef && hasTrunk:
		return fmt.Errorf("layer2AttachmentRef (untagged access port) and layer2Trunk (tagged trunk) are mutually exclusive")
	case !hasRef && !hasTrunk:
		return fmt.Errorf("layer2AttachmentRef or layer2Trunk is required when attachMode is %q", AttachModeL2)
	case hasRef && c.Layer2AttachmentRef.Name == "":
		return fmt.Errorf("layer2AttachmentRef.name is required when attachMode is %q", AttachModeL2)
	}
	if c.VRF != "" {
		return fmt.Errorf("vrf must not be set when attachMode is %q (the port is bridged, not routed)", AttachModeL2)
	}
	// A trunk port is never a bridge member itself — only its tagged
	// sub-interfaces are — so an address on the untagged port has no forwarding
	// path. The workload addresses its own VLAN sub-interfaces instead.
	if hasTrunk && len(c.IPAM) > 0 {
		return fmt.Errorf("ipam is not supported with layer2Trunk: the untagged port carries no traffic, address the VLAN sub-interfaces inside the workload")
	}
	if err := validateTrunk(c.Layer2Trunk); err != nil {
		return err
	}
	return nil
}

// validateTrunk rejects duplicate members and out-of-range or colliding
// workload-side VLAN ids. Members that inherit their id (vlan unset) can only
// be checked for collisions once their Layer2 is known, which the node agent
// does at merge time.
func validateTrunk(members []Layer2TrunkMember) error {
	seenRefs := make(map[string]struct{}, len(members))
	seenVLANs := make(map[uint16]struct{}, len(members))
	for i := range members {
		name := members[i].Name
		if name == "" {
			return fmt.Errorf("layer2Trunk[%d].name is required", i)
		}
		if _, dup := seenRefs[name]; dup {
			return fmt.Errorf("layer2Trunk references %q more than once", name)
		}
		seenRefs[name] = struct{}{}

		vlan := members[i].VLAN
		if vlan == nil {
			continue
		}
		if *vlan == 0 || *vlan > maxVLANID {
			return fmt.Errorf("layer2Trunk[%d].vlan %d is out of range (want 1-%d)", i, *vlan, maxVLANID)
		}
		if _, dup := seenVLANs[*vlan]; dup {
			return fmt.Errorf("layer2Trunk uses vlan %d more than once", *vlan)
		}
		seenVLANs[*vlan] = struct{}{}
	}
	return nil
}
