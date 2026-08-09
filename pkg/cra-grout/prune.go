package cra

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Interface is the subset of `grcli -j interface show` output the prune needs.
type Interface struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ifaceTypePort is the grout interface type of a DPDK port (as opposed to a
// bridge, vlan or vxlan interface).
const ifaceTypePort = "port"

// workloadPortName matches the CRA-side port names the workload CNI derives for
// a pod attachment: portName() renders "cra" + a hex hash and vhostPortName()
// renders "v" + a hex hash (see pkg/cni/datapath.go).
//
// This is the ownership test for pruning, so it is anchored on both ends rather
// than being a loose prefix match: ports grout has for other reasons -- above
// all the shared fabric trunk, created by the CRA's own network setup and never
// present in a rendered batch -- must not match, or the first reconcile would
// delete the node's uplink.
//
// The hash length is deliberately a range rather than the exact current value.
// Pinning it would couple this to a constant in another package, and the
// failure mode of drifting out of sync is silent: the prune would simply stop
// recognising its own ports and go back to leaking every one of them.
var workloadPortName = regexp.MustCompile(`^(cra|v)[0-9a-f]{4,32}$`)

// workloadTapPortAdd matches the batch line that creates a workload port backed
// by a tap grout makes itself, capturing the port name, the MTU and the VRF or
// bridge domain it binds to. vhost-user ports are deliberately not matched:
// their backing socket stays grout's, so re-applying one is harmless.
var workloadTapPortAdd = regexp.MustCompile(
	`^interface add port (\S+) devargs net_tap_\S+(?: mtu ([0-9]+))?(?: (vrf|domain) (\S+))?$`)

// TapPortAdd is what one `interface add port` line asks a workload tap port to
// be. MTU 0 and an empty Bind mean the line carries no such argument, which is
// grout's "keep the default" and cannot be compared against anything.
type TapPortAdd struct {
	Name     string
	MTU      uint16
	BindKind string
	BindName string
}

// WorkloadTapPortAdd reports whether line creates a workload port backed by a
// grout-created tap, and what it asks that port to be.
func WorkloadTapPortAdd(line string) (TapPortAdd, bool) {
	m := workloadTapPortAdd.FindStringSubmatch(strings.TrimSpace(line))
	if len(m) != 5 || !workloadPortName.MatchString(m[1]) {
		return TapPortAdd{}, false
	}
	add := TapPortAdd{Name: m[1], BindKind: m[3], BindName: m[4]}
	if m[2] != "" {
		parsed, err := strconv.ParseUint(m[2], 10, 16)
		if err != nil {
			return TapPortAdd{}, false
		}
		add.MTU = uint16(parsed)
	}
	return add, true
}

// InterfaceDetail is the subset of `grcli -j interface show NAME` this needs.
//
// It is a different shape from the list form above, and deliberately queried
// separately: the list prints one row per interface with no MTU column at all,
// and reports a VRF-mode interface's VRF under "domain". Only the single
// interface view carries the MTU, and only it separates "vrf" from "domain".
type InterfaceDetail struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	VRF    string `json:"vrf"`
	Domain string `json:"domain"`
	MTU    uint16 `json:"mtu"`
}

// ParseInterfaceDetail decodes the output of `grcli -j interface show NAME`.
func ParseInterfaceDetail(out []byte) (*InterfaceDetail, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil //nolint:nilnil // "grout printed nothing" is not an error, and there is no detail to return.
	}
	var detail InterfaceDetail
	if err := json.Unmarshal([]byte(trimmed), &detail); err != nil {
		return nil, fmt.Errorf("parsing grcli interface detail: %w", err)
	}
	return &detail, nil
}

// The interface modes grout reports. gr_iface_mode_name spells the VRF one in
// capitals and the others in lower case, so they are compared case-insensitively
// rather than trusting that. The modes a workload port is never rendered with --
// "XC" and "bond" -- are deliberately absent: they are not something to match
// against, they are something to reject.
const (
	ifaceModeVRF    = "vrf"
	ifaceModeBridge = "bridge"
)

// Mismatch describes how a live port differs from what the batch line asks for,
// or is empty when the two agree. Properties grout did not report are not
// mismatches: an adopted tap cannot be reconfigured anyway -- its netdev is the
// pod's -- so the only thing a false mismatch could achieve is to take the
// node's whole configuration down over a change in grout's CLI output.
func (a TapPortAdd) Mismatch(live *InterfaceDetail) string {
	if live == nil {
		return ""
	}
	if a.MTU != 0 && live.MTU != 0 && a.MTU != live.MTU {
		return fmt.Sprintf("MTU is %d, not %d", live.MTU, a.MTU)
	}

	// A line with no trailing binding is not "unbound": grout puts the port in
	// the default VRF. So the mode is checked either way, and only the name is
	// conditional on the line having asked for one.
	//
	// The wanted mode is matched exactly rather than as "VRF or not". A bridged
	// port and an XC or bonded one are all non-VRF, but only one of them is
	// what a "domain" line asked for, and the other two would then be waved
	// through on a port whose datapath behaviour is nothing like the intent.
	wantVRF := a.BindKind != "domain"
	wantMode := ifaceModeVRF
	if !wantVRF {
		wantMode = ifaceModeBridge
	}
	if live.Mode != "" && !strings.EqualFold(live.Mode, wantMode) {
		return fmt.Sprintf("mode is %q, not %q", live.Mode, wantMode)
	}

	// No rendered binding means the default VRF specifically, so a port sitting
	// in a tenant VRF is drift just as much as one in the wrong tenant VRF --
	// it would keep forwarding the workload's traffic in the old routing
	// domain. The accepted spellings are the ones isDefaultVRF recognises.
	if a.BindName == "" {
		if live.VRF != "" && !isDefaultVRF(live.VRF) {
			return fmt.Sprintf("vrf is %q, not the default VRF", live.VRF)
		}
		return ""
	}

	bound := live.VRF
	if !wantVRF {
		bound = live.Domain
	}
	if bound != "" && bound != a.BindName {
		return fmt.Sprintf("%s is %q, not %q", a.BindKind, bound, a.BindName)
	}
	return ""
}

// isDefaultVRF reports whether name denotes grout's default VRF, which a port
// with no rendered binding lands in. It mirrors the workload CNI helper of the
// same name; grout's own reserved spelling is GR_DEFAULT_VRF_NAME, "main".
func isDefaultVRF(name string) bool {
	switch strings.ToLower(name) {
	case "", "default", "main":
		return true
	default:
		return false
	}
}

// LiveWorkloadPorts returns the names of the workload ports grout currently
// holds: the set a replay must not try to create again.
func LiveWorkloadPorts(live []Interface) map[string]bool {
	ports := map[string]bool{}
	for i := range live {
		if live[i].Type == ifaceTypePort && workloadPortName.MatchString(live[i].Name) {
			ports[live[i].Name] = true
		}
	}
	return ports
}

// ifaceTypeVlan is the grout interface type of a VLAN sub-interface, which is
// what one tagged member of a trunk port renders as.
const ifaceTypeVlan = "vlan"

// addIfaceLine matches the interface-creating lines of a rendered batch that
// this prune owns: the DPDK ports, and the VLAN sub-interfaces hung off them.
//
// The other object kinds a batch creates (vrf, bridge, vxlan) are node-level
// state, not per-workload state, so they are never pruned.
var addIfaceLine = regexp.MustCompile(`^interface add (port|vlan) (\S+) `)

// vlanIfaceName matches the name of a VLAN sub-interface of a workload port,
// i.e. "<port>.<vlan>". The parent half is held to the same ownership test as a
// port name so the fabric trunk's own sub-interfaces -- created by the CRA's
// network setup and never present in a rendered batch -- are not deleted.
var vlanIfaceName = regexp.MustCompile(`^(cra|v)[0-9a-f]{4,32}\.[0-9]{1,4}$`)

// DesiredInterfaceNames extracts the set of workload interfaces a rendered batch
// declares: the ports themselves and the trunk VLAN sub-interfaces above them.
func DesiredInterfaceNames(batch string) map[string]bool {
	desired := map[string]bool{}
	for _, raw := range strings.Split(batch, "\n") {
		if m := addIfaceLine.FindStringSubmatch(strings.TrimSpace(raw)); m != nil {
			desired[m[2]] = true
		}
	}
	return desired
}

// ParseInterfaces decodes the output of `grcli -j interface show`.
func ParseInterfaces(out []byte) ([]Interface, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	var ifaces []Interface
	if err := json.Unmarshal([]byte(trimmed), &ifaces); err != nil {
		return nil, fmt.Errorf("parsing grcli interface output: %w", err)
	}
	return ifaces, nil
}

// StaleInterfaces returns the workload interfaces that exist in grout but are
// absent from the desired state, in the order they must be deleted.
//
// This is the delete half of the reconcile. The rendered batch is a full
// desired-state replay applied with create-only commands, so without an
// explicit prune a port removed from the NodeNetworkConfig -- which is what a
// deleted pod amounts to -- simply stays in the fast path forever, holding its
// tap or vhost-user socket, its gateway address and its host routes. The leak
// is invisible from Kubernetes: the pod is gone and grout still forwards for it.
//
// VLAN sub-interfaces are returned before ports, and that ordering is required
// rather than cosmetic: grout refuses to delete an interface that still has
// sub-interfaces (iface_destroy returns EBUSY on a non-empty `subinterfaces`),
// and it does not cascade. Deleting a trunk port first would fail, and because
// EBUSY is not "already gone" the whole reconcile would then error out on every
// pass -- leaving the pod's port live forever. Within each group the names are
// sorted so application and logging are deterministic.
func StaleInterfaces(desired map[string]bool, live []Interface) []string {
	var vlans, ports []string
	for i := range live {
		iface := &live[i]
		if desired[iface.Name] {
			continue
		}
		switch {
		case iface.Type == ifaceTypeVlan && vlanIfaceName.MatchString(iface.Name):
			vlans = append(vlans, iface.Name)
		case iface.Type == ifaceTypePort && workloadPortName.MatchString(iface.Name):
			ports = append(ports, iface.Name)
		}
	}
	sort.Strings(vlans)
	sort.Strings(ports)
	return append(vlans, ports...)
}
