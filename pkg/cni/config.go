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
	// defaultSocketMode is assumed when neither the config nor the device
	// plugin's device-info file states a mode: the workload runs the server
	// socket and vSR connects to it, matching the reference manifests.
	defaultSocketMode = SocketModeServer
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

	// Transport selects the CRA-side wiring:
	//   - "veth" (default): a veth pair whose CRA-side end is moved into the CRA
	//     netns.
	//   - "vhostuser": a DPDK/virtio-user vhost-user socket (VSR-only, rendered
	//     as an fpvhost fast-path virtual-port).
	Transport string `json:"transport,omitempty"`

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

	// SocketPath overrides the host-side vhost-user unix socket path that is
	// otherwise derived from the device-plugin allocation. It never replaces the
	// allocation itself: an attachment with no deviceID is rejected outright.
	// Only meaningful for "vhostuser".
	SocketPath string `json:"socketPath,omitempty"`

	// SocketMode is the vhost-user socket mode from the workload's perspective
	// ("client" or "server"). It is only a fallback: the device plugin's own
	// device-info file states the mode it allocated and wins over this value.
	// VSR inverts the resulting mode when rendering the fpvhost virtual-port.
	SocketMode string `json:"socketMode,omitempty"`

	// DeviceResource is the device-plugin resource the attachment is allocated
	// from, i.e. the value of the NAD's k8s.v1.cni.cncf.io/resourceName
	// annotation. It selects which of the two 6WIND socket trees holds the
	// host-side and which the pod-side path; it does not allocate anything.
	// Defaults to nc-k8s-plugin.6wind.com/virtio-user.
	DeviceResource string `json:"deviceResource,omitempty"`

	// AgentSocket overrides the unix socket the plugin uses to reach the
	// node-local CRA agent (workloadcni.DefaultSocketPath when empty). The plugin
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
	// IPAM is required for routed veth attachments (the pod-side address is
	// relayed to the guest). It is optional for vhost-user, whose addressing may
	// be guest-side, and in the L2 attach mode, where the workload is addressed
	// inside the shared L2 domain rather than by this plugin. When it is
	// configured it is always delegated and applied.
	if len(conf.IPAM) == 0 {
		if !conf.isVhostUser() && !conf.isL2() {
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
	switch c.transport() {
	case TransportVeth, TransportVhostUser:
	default:
		return fmt.Errorf("invalid transport %q (want %q or %q)", c.Transport, TransportVeth, TransportVhostUser)
	}

	if c.isL2() {
		if err := c.validateL2Attach(); err != nil {
			return err
		}
	} else if len(c.Layer2Trunk) > 0 || c.Layer2AttachmentRef != nil {
		return fmt.Errorf("layer2AttachmentRef and layer2Trunk are only valid when attachMode is %q", AttachModeL2)
	}

	if c.isVhostUser() && c.SocketMode != "" {
		// socketPath and socketMode are both optional: they are normally derived
		// from the device-plugin allocation (see deviceplugin.go). A stated mode
		// must still be one of the two valid values.
		switch c.SocketMode {
		case SocketModeClient, SocketModeServer:
		default:
			return fmt.Errorf("socketMode must be %q or %q when transport is %q",
				SocketModeClient, SocketModeServer, TransportVhostUser)
		}
	}
	if !c.isVhostUser() && (c.SocketPath != "" || c.SocketMode != "" || c.DeviceResource != "") {
		return fmt.Errorf("socketPath, socketMode and deviceResource are only valid when transport is %q",
			TransportVhostUser)
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

// socketMode returns the configured workload-side vhost-user socket mode or the
// default. The device plugin's device-info file overrides it when present.
func (c *NetConf) socketMode() string {
	if c.SocketMode != "" {
		return c.SocketMode
	}
	return defaultSocketMode
}
