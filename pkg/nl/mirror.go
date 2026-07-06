//nolint:wrapcheck
package nl

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/vishvananda/netlink"
	vnl "github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

const (
	greMirrorPrefix    = "gre-"
	gretapMirrorPrefix = "gtap-"

	handleMinIngress = netlink.HANDLE_MIN_INGRESS
	handleMinEgress  = netlink.HANDLE_MIN_EGRESS

	// clsactHandleMajor is the fixed major number (0xffff) of a clsact qdisc; the
	// handle is 0xffff0000. The clsact parent magic (HANDLE_CLSACT) must NOT be
	// used as the handle or the kernel rejects the qdisc with EINVAL.
	clsactHandleMajor = 0xffff

	// mirrorFilterPriorityBase is the base tc filter priority for mirror filters.
	// Mirror filters share the clsact qdisc with the node's forwarding/BPF data
	// path, so they MUST live in a dedicated high priority range and only ever
	// delete their own filters — never the forwarding filters that occupy the low
	// priorities. Touching those breaks IPv4/IPv6 forwarding on the interface.
	mirrorFilterPriorityBase = 0x8000

	maxMirrorIfNameLen = 15

	// bitsPerByte is used to convert a byte length into a CIDR prefix length.
	bitsPerByte = 8

	ethPAll  = uint16(0x0003) // ETH_P_ALL
	ethPIP   = uint16(0x0800) // ETH_P_IP
	ethPIPv6 = uint16(0x86DD) // ETH_P_IPV6

	greKeyFlag = uint16(0x2000) // GRE_KEY present flag

	protoTCP    = 6
	protoUDP    = 17
	protoICMP   = 1
	protoSCTP   = 132
	protoICMPv6 = 58
)

// ReconcileMirror programs the full mirror data path described by the
// NetlinkConfiguration: per-VRF loopbacks, GRE tunnels and tc mirror filters.
// Stale mirror resources that are no longer desired — GRE tunnels, per-VRF
// loopbacks and tc mirror filters — are removed by CleanupMirrors.
func (n *Manager) ReconcileMirror(cfg *NetlinkConfiguration) error {
	if err := n.ReconcileLoopbacks(cfg.Loopbacks); err != nil {
		return fmt.Errorf("error reconciling loopbacks: %w", err)
	}

	tunnelIndex, err := n.ReconcileGRETunnels(cfg.GRETunnels)
	if err != nil {
		return fmt.Errorf("error reconciling GRE tunnels: %w", err)
	}

	if err := n.ReconcileTcMirrors(cfg.Mirrors, tunnelIndex); err != nil {
		return fmt.Errorf("error reconciling tc mirrors: %w", err)
	}

	if err := n.CleanupMirrors(cfg.GRETunnels, cfg.Loopbacks, cfg.Mirrors); err != nil {
		return fmt.Errorf("error cleaning up mirrors: %w", err)
	}

	return nil
}

// ReconcileLoopbacks ensures the desired loopback (dummy) interfaces exist in
// their VRFs with the correct addresses.
func (n *Manager) ReconcileLoopbacks(desired []LoopbackConfig) error {
	for i := range desired {
		if err := n.ensureLoopback(&desired[i]); err != nil {
			return fmt.Errorf("error ensuring loopback %s: %w", desired[i].Name, err)
		}
	}
	return nil
}

func (n *Manager) ensureLoopback(cfg *LoopbackConfig) error {
	if len(cfg.Name) > maxMirrorIfNameLen {
		return fmt.Errorf("loopback name %q exceeds max length %d", cfg.Name, maxMirrorIfNameLen)
	}

	vrfLink, err := n.toolkit.LinkByName(cfg.VRF)
	if err != nil {
		return fmt.Errorf("VRF %q not found: %w", cfg.VRF, err)
	}

	link, lookupErr := n.toolkit.LinkByName(cfg.Name)
	if lookupErr == nil && !isDesiredLoopback(link, vrfLink.Attrs().Index) {
		// An interface with this name exists but is not a dummy enslaved to the
		// desired VRF (e.g. it was left in a different VRF after a change, or is a
		// different link type). Replace it so the loopback ends up in the right VRF;
		// otherwise GRE source-address resolution could break.
		if err := n.toolkit.LinkDel(link); err != nil {
			return fmt.Errorf("error replacing mismatched loopback %s: %w", cfg.Name, err)
		}
		link, lookupErr = nil, errLoopbackRecreate
	}
	if lookupErr != nil {
		dummy := &netlink.Dummy{
			LinkAttrs: netlink.LinkAttrs{
				Name:        cfg.Name,
				MasterIndex: vrfLink.Attrs().Index,
			},
		}
		if err := n.toolkit.LinkAdd(dummy); err != nil {
			return fmt.Errorf("error creating dummy %s: %w", cfg.Name, err)
		}
		link, err = n.toolkit.LinkByName(cfg.Name)
		if err != nil {
			return fmt.Errorf("error fetching created dummy %s: %w", cfg.Name, err)
		}
	}

	if err := n.toolkit.LinkSetUp(link); err != nil {
		return fmt.Errorf("error setting up %s: %w", cfg.Name, err)
	}

	return n.reconcileLoopbackAddresses(link, cfg)
}

// errLoopbackRecreate is a sentinel used internally to route a mismatched loopback
// into the (re)creation path.
var errLoopbackRecreate = fmt.Errorf("loopback needs recreation")

// isDesiredLoopback reports whether an existing link is a dummy interface enslaved
// to the desired VRF.
func isDesiredLoopback(link netlink.Link, vrfIndex int) bool {
	return link.Type() == "dummy" && link.Attrs().MasterIndex == vrfIndex
}

// reconcileLoopbackAddresses makes the loopback carry exactly the desired
// addresses: missing ones are added and stale ones (e.g. left over after a
// subnet change) are removed, so GRE source selection always uses a valid
// address.
func (n *Manager) reconcileLoopbackAddresses(link netlink.Link, cfg *LoopbackConfig) error {
	desired := make([]*netlink.Addr, 0, len(cfg.Addresses))
	for _, addrStr := range cfg.Addresses {
		addr, err := n.toolkit.ParseAddr(addrStr)
		if err != nil {
			return fmt.Errorf("error parsing address %s: %w", addrStr, err)
		}
		desired = append(desired, addr)
	}

	currentList, err := n.toolkit.AddrList(link, unix.AF_UNSPEC)
	if err != nil {
		return fmt.Errorf("error listing addresses on %s: %w", cfg.Name, err)
	}
	current := make([]*netlink.Addr, 0, len(currentList))
	for i := range currentList {
		// Only manage global addresses; leave kernel-managed link-local addresses
		// (e.g. IPv6 fe80::/10) untouched.
		if currentList[i].IP.IsLinkLocalUnicast() {
			continue
		}
		current = append(current, &currentList[i])
	}

	for _, addr := range desired {
		if !containsNetlinkAddress(current, addr) {
			if err := n.toolkit.AddrAdd(link, addr); err != nil && !errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("error adding address %s to %s: %w", addr, cfg.Name, err)
			}
		}
	}
	for _, addr := range current {
		if !containsNetlinkAddress(desired, addr) {
			if err := n.toolkit.AddrDel(link, addr); err != nil {
				return fmt.Errorf("error removing stale address %s from %s: %w", addr, cfg.Name, err)
			}
		}
	}
	return nil
}

// ReconcileGRETunnels creates the desired GRE tunnels inside their VRFs and
// returns a map of GRE interface name to its ifindex.
func (n *Manager) ReconcileGRETunnels(tunnels []GRETunnel) (map[string]int, error) {
	tunnelIndex := make(map[string]int, len(tunnels))
	for i := range tunnels {
		idx, err := n.ensureGRETunnel(&tunnels[i])
		if err != nil {
			return nil, fmt.Errorf("error ensuring GRE tunnel %s: %w", tunnels[i].Name, err)
		}
		tunnelIndex[tunnels[i].Name] = idx
	}
	return tunnelIndex, nil
}

func (n *Manager) ensureGRETunnel(t *GRETunnel) (int, error) {
	if len(t.Name) > maxMirrorIfNameLen {
		return 0, fmt.Errorf("GRE tunnel name %q exceeds max length %d", t.Name, maxMirrorIfNameLen)
	}

	localIP := net.ParseIP(t.Local)
	remoteIP := net.ParseIP(t.Remote)
	if localIP == nil || remoteIP == nil {
		return 0, fmt.Errorf("invalid GRE IPs: local=%s remote=%s", t.Local, t.Remote)
	}
	if (localIP.To4() == nil) != (remoteIP.To4() == nil) {
		return 0, fmt.Errorf("GRE local %s and remote %s must be the same address family", t.Local, t.Remote)
	}

	vrfLink, err := n.toolkit.LinkByName(t.VRF)
	if err != nil {
		return 0, fmt.Errorf("VRF %q not found for GRE tunnel: %w", t.VRF, err)
	}

	// Bind the tunnel to the interface that owns the source address so the kernel
	// resolves the local address in the correct VRF (l3mdev) domain.
	linkIndex := 0
	if t.SourceInterface != "" {
		srcLink, srcErr := n.toolkit.LinkByName(t.SourceInterface)
		if srcErr != nil {
			return 0, fmt.Errorf("source interface %q not found for GRE tunnel: %w", t.SourceInterface, srcErr)
		}
		linkIndex = srcLink.Attrs().Index
	}

	desired := greLink(t, vrfLink.Attrs().Index, linkIndex, localIP, remoteIP)

	// If a tunnel with this name already exists, keep it only when it still matches
	// the desired endpoints/key/VRF. The interface name is derived from the
	// MirrorTarget name (not its mutable fields), so an edit to the collector IP or
	// tunnel key would otherwise leave a stale tunnel pointing at the old collector.
	if existing, lookupErr := n.toolkit.LinkByName(t.Name); lookupErr == nil {
		if greTunnelUpToDate(existing, desired) {
			return existing.Attrs().Index, nil
		}
		if err := n.toolkit.LinkDel(existing); err != nil {
			return 0, fmt.Errorf("error replacing outdated GRE tunnel %s: %w", t.Name, err)
		}
	}

	if err := n.toolkit.LinkAdd(desired); err != nil {
		return 0, fmt.Errorf("error creating GRE tunnel %s: %w", t.Name, err)
	}

	link, err := n.toolkit.LinkByName(t.Name)
	if err != nil {
		return 0, fmt.Errorf("error fetching created GRE tunnel %s: %w", t.Name, err)
	}
	if err := n.toolkit.LinkSetUp(link); err != nil {
		return 0, fmt.Errorf("error setting up GRE tunnel %s: %w", t.Name, err)
	}
	return link.Attrs().Index, nil
}

// greTunnelUpToDate reports whether an existing GRE link still matches the desired
// tunnel's family/encapsulation, endpoints, key and VRF. The source link binding
// (IFLA_GRE_LINK) is intentionally not compared: the kernel does not report it
// back, so comparing it would force needless tunnel recreation on every reconcile.
func greTunnelUpToDate(existing, desired netlink.Link) bool {
	if existing.Attrs().MasterIndex != desired.Attrs().MasterIndex {
		return false
	}
	switch d := desired.(type) {
	case *netlink.Gretun:
		e, ok := existing.(*netlink.Gretun)
		return ok && e.Local.Equal(d.Local) && e.Remote.Equal(d.Remote) &&
			e.IKey == d.IKey && e.OKey == d.OKey
	case *netlink.Gretap:
		e, ok := existing.(*netlink.Gretap)
		return ok && e.Local.Equal(d.Local) && e.Remote.Equal(d.Remote) &&
			e.IKey == d.IKey && e.OKey == d.OKey
	default:
		return false
	}
}

// greLink builds the netlink GRE/GRETAP link for a tunnel. The netlink library
// selects the kind from the address family of the endpoints: IPv4 endpoints yield
// "gre"/"gretap", IPv6 endpoints yield "ip6gre"/"ip6gretap" (IP6GRE). The endpoints
// are validated to share a family by the caller. linkIndex, when non-zero, binds
// the tunnel to the device owning the source address (IFLA_GRE_LINK).
func greLink(t *GRETunnel, masterIndex, linkIndex int, local, remote net.IP) netlink.Link {
	attrs := netlink.LinkAttrs{Name: t.Name, MasterIndex: masterIndex}
	var iKey, oKey uint32
	var iFlags, oFlags uint16
	if t.Key != nil {
		iKey = *t.Key
		oKey = *t.Key
		iFlags = greKeyFlag
		oFlags = greKeyFlag
	}
	if t.Layer2 {
		return &netlink.Gretap{
			LinkAttrs: attrs,
			Local:     local,
			Remote:    remote,
			Link:      uint32(linkIndex), //nolint:gosec // interface index is non-negative
			IKey:      iKey,
			OKey:      oKey,
			IFlags:    iFlags,
			OFlags:    oFlags,
		}
	}
	return &netlink.Gretun{
		LinkAttrs: attrs,
		Local:     local,
		Remote:    remote,
		Link:      uint32(linkIndex), //nolint:gosec // interface index is non-negative
		IKey:      iKey,
		OKey:      oKey,
		IFlags:    iFlags,
		OFlags:    oFlags,
	}
}

// ReconcileTcMirrors sets up tc clsact qdiscs and flower filters on the source
// interfaces to mirror matching traffic to GRE tunnels.
func (n *Manager) ReconcileTcMirrors(rules []MirrorRule, tunnelIndex map[string]int) error {
	grouped := map[string][]MirrorRule{}
	for i := range rules {
		grouped[rules[i].SourceInterface] = append(grouped[rules[i].SourceInterface], rules[i])
	}

	for iface := range grouped {
		if err := n.setupMirrorFilters(iface, grouped[iface], tunnelIndex); err != nil {
			return fmt.Errorf("error setting up mirror on %s: %w", iface, err)
		}
	}
	return nil
}

func (n *Manager) setupMirrorFilters(iface string, rules []MirrorRule, tunnelIndex map[string]int) error {
	link, err := n.toolkit.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("source interface %q not found: %w", iface, err)
	}

	if err := n.ensureClsactQdisc(link); err != nil {
		return err
	}
	if err := n.clearMirrorFilters(link, false); err != nil {
		return err
	}

	// Priorities are scoped per clsact hook (ingress/egress), so track a separate
	// counter per parent. A single MirrorRule can expand into several flower
	// filters (one per IP family / L4 protocol), each needing a unique priority.
	prioByParent := map[uint32]int{handleMinIngress: 0, handleMinEgress: 0}
	for i := range rules {
		r := &rules[i]
		greIdx, ok := tunnelIndex[r.GREInterface]
		if !ok {
			return fmt.Errorf("no GRE tunnel index for interface %s", r.GREInterface)
		}
		matches := buildMirrorMatches(r)
		for _, parent := range mirrorParents(r.Direction, r.WorkloadFacing) {
			for j := range matches {
				prio, err := nextMirrorPriority(iface, parent, prioByParent)
				if err != nil {
					return err
				}
				if err := n.addMirrorFilter(link, parent, prio, r, &matches[j], greIdx); err != nil {
					return fmt.Errorf("error adding mirror filter: %w", err)
				}
			}
		}
	}
	return nil
}

// mirrorParents maps a workload-perspective direction to the tc clsact hook(s) on
// the source interface. "ingress" means traffic to the workload, "egress" means
// traffic from the workload. For a workload-facing interface (the L2 `vlan.<id>`
// access port) the hooks are inverted: traffic to the workload leaves the bridge
// via the port's egress hook, and traffic from the workload arrives via its ingress
// hook. For a fabric-facing interface (the `vx.<vrf>` VXLAN port) the natural
// mapping applies (from-fabric arrives on ingress, to-fabric leaves on egress).
func mirrorParents(direction string, workloadFacing bool) []uint32 {
	var toWorkloadHook, fromWorkloadHook uint32 = handleMinIngress, handleMinEgress
	if workloadFacing {
		toWorkloadHook, fromWorkloadHook = handleMinEgress, handleMinIngress
	}
	switch direction {
	case directionIngress: // to the workload
		return []uint32{toWorkloadHook}
	case directionEgress: // from the workload
		return []uint32{fromWorkloadHook}
	case directionBoth:
		return []uint32{handleMinIngress, handleMinEgress}
	default:
		return nil
	}
}

// nextMirrorPriority returns the next free tc filter priority for the given hook,
// staying within the dedicated mirror priority range, and bumps the counter.
func nextMirrorPriority(iface string, parent uint32, prioByParent map[uint32]int) (uint16, error) {
	p := mirrorFilterPriorityBase + prioByParent[parent]
	if p > int(^uint16(0)) {
		return 0, fmt.Errorf("too many mirror filters on %s (parent %#x): priority overflow", iface, parent)
	}
	prioByParent[parent]++
	return uint16(p), nil //nolint:gosec // bounds-checked above
}

// mirrorMatch is one concrete (IP family, L4 protocol) combination a MirrorRule
// expands into. A nil proto means "no L4 protocol match" (match the whole family).
type mirrorMatch struct {
	ethType uint16
	proto   *vnl.IPProto
}

// buildMirrorMatches expands a MirrorRule into the concrete flower matches to
// program. The IP family is derived from the protocol (icmp→v4, icmpv6→v6) or the
// prefixes; when it cannot be determined, both families are emitted. When ports
// are matched without an explicit protocol, one match per port-based protocol
// (TCP/UDP/SCTP) is emitted so the port match is actually honored instead of
// silently degrading to match-all.
func buildMirrorMatches(rule *MirrorRule) []mirrorMatch {
	families := mirrorFamilies(rule)
	protos := mirrorProtos(rule)
	matches := make([]mirrorMatch, 0, len(families)*len(protos))
	for _, fam := range families {
		for _, proto := range protos {
			matches = append(matches, mirrorMatch{ethType: fam, proto: proto})
		}
	}
	return matches
}

// mirrorFamilies returns the IP families (as ethertypes) a rule applies to.
func mirrorFamilies(rule *MirrorRule) []uint16 {
	switch strings.ToLower(rule.Protocol) {
	case "icmp":
		return []uint16{ethPIP}
	case "icmpv6":
		return []uint16{ethPIPv6}
	}
	hasV4 := prefixFamily(rule.SrcPrefix) == ethPIP || prefixFamily(rule.DstPrefix) == ethPIP
	hasV6 := prefixFamily(rule.SrcPrefix) == ethPIPv6 || prefixFamily(rule.DstPrefix) == ethPIPv6
	switch {
	case hasV6 && !hasV4:
		return []uint16{ethPIPv6}
	case hasV4 && !hasV6:
		return []uint16{ethPIP}
	default:
		// No (or mixed) prefix family information: program both families so an
		// IPv6 flow is never silently missed.
		return []uint16{ethPIP, ethPIPv6}
	}
}

// mirrorProtos returns the L4 protocols a rule matches. An explicit protocol is
// used as-is; ports without a protocol expand to TCP/UDP/SCTP; otherwise no L4
// match is applied.
func mirrorProtos(rule *MirrorRule) []*vnl.IPProto {
	if rule.Protocol != "" {
		if proto := ipProtoNumber(rule.Protocol); proto != nil {
			return []*vnl.IPProto{proto}
		}
		// Unknown protocol string: fall back to no L4 match rather than dropping
		// the rule entirely.
		return []*vnl.IPProto{nil}
	}
	if rule.SrcPort > 0 || rule.DstPort > 0 {
		return []*vnl.IPProto{ipProtoPtr(protoTCP), ipProtoPtr(protoUDP), ipProtoPtr(protoSCTP)}
	}
	return []*vnl.IPProto{nil}
}

const (
	directionIngress = "ingress"
	directionEgress  = "egress"
	directionBoth    = "both"
)

func (n *Manager) ensureClsactQdisc(link netlink.Link) error {
	qdiscs, err := n.toolkit.QdiscList(link)
	if err != nil {
		return fmt.Errorf("error listing qdiscs: %w", err)
	}
	for _, q := range qdiscs {
		if q.Type() == "clsact" {
			return nil
		}
	}

	qdisc := &netlink.Clsact{
		QdiscAttrs: netlink.QdiscAttrs{
			LinkIndex: link.Attrs().Index,
			Handle:    netlink.MakeHandle(clsactHandleMajor, 0),
			Parent:    netlink.HANDLE_CLSACT,
		},
	}
	if err := n.toolkit.QdiscAdd(qdisc); err != nil {
		return fmt.Errorf("error adding clsact qdisc: %w", err)
	}
	return nil
}

// clearMirrorFilters removes only the mirror filters (those in the dedicated
// mirror priority range) from the link's clsact hooks, leaving the node's
// forwarding/BPF filters — which occupy the low priorities — untouched.
//
// tolerateListErr controls how a FilterList failure is handled: callers that have
// just ensured the clsact qdisc (the setup path) pass false so a list failure is
// surfaced instead of silently leaving stale filters behind; cleanup callers pass
// true because the interface may legitimately have no clsact qdisc to list.
func (n *Manager) clearMirrorFilters(link netlink.Link, tolerateListErr bool) error {
	for _, parent := range []uint32{handleMinIngress, handleMinEgress} {
		filters, err := n.toolkit.FilterList(link, parent)
		if err != nil {
			if tolerateListErr {
				continue
			}
			return fmt.Errorf("error listing filters on %s (parent %#x): %w", link.Attrs().Name, parent, err)
		}
		for _, f := range filters {
			if int(f.Attrs().Priority) < mirrorFilterPriorityBase {
				continue
			}
			if err := n.toolkit.FilterDel(f); err != nil {
				return fmt.Errorf("error deleting mirror filter on %s: %w", link.Attrs().Name, err)
			}
		}
	}
	return nil
}

func (n *Manager) addMirrorFilter(link netlink.Link, parent uint32, prio uint16, rule *MirrorRule, match *mirrorMatch, greIfIndex int) error {
	flower := &netlink.Flower{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: link.Attrs().Index,
			Parent:    parent,
			Priority:  prio,
			Protocol:  ethPAll,
		},
		EthType: match.ethType,
		Actions: []netlink.Action{
			&netlink.MirredAction{
				ActionAttrs:  netlink.ActionAttrs{Action: netlink.TC_ACT_PIPE},
				MirredAction: netlink.TCA_EGRESS_MIRROR,
				Ifindex:      greIfIndex,
			},
		},
	}

	if match.proto != nil {
		flower.IPProto = match.proto
	}
	// Only program a prefix whose family matches this filter's ethertype; a
	// cross-family prefix (e.g. an IPv6 dst on an IPv4 filter) cannot be encoded.
	if prefixFamily(rule.SrcPrefix) == match.ethType {
		if ip, mask, ok := parseHostOrCIDR(rule.SrcPrefix); ok {
			flower.SrcIP = ip
			flower.SrcIPMask = mask
		}
	}
	if prefixFamily(rule.DstPrefix) == match.ethType {
		if ip, mask, ok := parseHostOrCIDR(rule.DstPrefix); ok {
			flower.DestIP = ip
			flower.DestIPMask = mask
		}
	}
	// Ports are only encoded by the kernel for port-based protocols (TCP/UDP/SCTP),
	// which is exactly when match.proto is one of those.
	if isPortProto(match.proto) {
		if rule.SrcPort > 0 {
			flower.SrcPort = rule.SrcPort
		}
		if rule.DstPort > 0 {
			flower.DestPort = rule.DstPort
		}
	}

	if err := n.toolkit.FilterAdd(flower); err != nil {
		return fmt.Errorf("error adding flower filter: %w", err)
	}
	return nil
}

// CleanupMirrors removes stale GRE tunnels, stale per-VRF loopbacks and stale
// mirror filters that are no longer in the desired state. GRE tunnels are
// identified by the mirror naming prefixes; loopbacks are identified as dummy
// interfaces enslaved to a VRF (the only VRF-enslaved dummies in this data path
// are mirror loopbacks). Only mirror filters (in the dedicated mirror priority
// range) are removed from mirror-shaped source interfaces (l2.*, vx.*) that are
// no longer referenced by any rule; the shared clsact qdisc and the node's
// forwarding/BPF filters are never touched.
func (n *Manager) CleanupMirrors(tunnels []GRETunnel, loopbacks []LoopbackConfig, rules []MirrorRule) error {
	links, err := n.toolkit.LinkList()
	if err != nil {
		return fmt.Errorf("error listing links: %w", err)
	}

	desiredGRE := make(map[string]struct{}, len(tunnels))
	for i := range tunnels {
		desiredGRE[tunnels[i].Name] = struct{}{}
	}
	desiredLoopbacks := make(map[string]struct{}, len(loopbacks))
	for i := range loopbacks {
		desiredLoopbacks[loopbacks[i].Name] = struct{}{}
	}
	desiredSources := make(map[string]struct{}, len(rules))
	for i := range rules {
		desiredSources[rules[i].SourceInterface] = struct{}{}
	}
	mirrorVRFIndices := mirrorVRFIndexSet(links, tunnels, loopbacks)

	for _, link := range links {
		if err := n.cleanupMirrorLink(link, desiredGRE, desiredSources, desiredLoopbacks, mirrorVRFIndices); err != nil {
			return err
		}
	}
	return nil
}

// cleanupMirrorLink removes a single link's stale mirror state: stale GRE tunnels
// and stale (mirror-VRF-scoped) loopbacks are deleted; mirror filters are cleared
// from source interfaces no longer referenced by any rule.
func (n *Manager) cleanupMirrorLink(link netlink.Link, desiredGRE, desiredSources, desiredLoopbacks map[string]struct{}, mirrorVRFIndices map[int]struct{}) error {
	name := link.Attrs().Name
	switch {
	case isMirrorGREName(name):
		if _, ok := desiredGRE[name]; !ok {
			if err := n.toolkit.LinkDel(link); err != nil {
				return fmt.Errorf("error deleting stale GRE %s: %w", name, err)
			}
		}
	case isMirrorSourceName(name):
		if _, ok := desiredSources[name]; !ok {
			return n.clearMirrorFilters(link, true)
		}
	case isMirrorLoopback(link, mirrorVRFIndices):
		if _, ok := desiredLoopbacks[name]; !ok {
			if err := n.toolkit.LinkDel(link); err != nil {
				return fmt.Errorf("error deleting stale loopback %s: %w", name, err)
			}
		}
	}
	return nil
}

// mirrorVRFIndexSet returns the ifindexes of VRFs that currently carry mirror
// config (a desired GRE tunnel or loopback). Only dummies enslaved to these are
// candidates for loopback GC, so a dummy in an unrelated VRF (management, cluster,
// fabric, ...) is never deleted — important because CleanupMirrors runs every
// reconcile and LinkDel is destructive.
func mirrorVRFIndexSet(links []netlink.Link, tunnels []GRETunnel, loopbacks []LoopbackConfig) map[int]struct{} {
	mirrorVRFNames := make(map[string]struct{}, len(tunnels)+len(loopbacks))
	for i := range tunnels {
		mirrorVRFNames[tunnels[i].VRF] = struct{}{}
	}
	for i := range loopbacks {
		mirrorVRFNames[loopbacks[i].VRF] = struct{}{}
	}
	indices := make(map[int]struct{})
	for _, link := range links {
		if link.Type() != "vrf" {
			continue
		}
		if _, ok := mirrorVRFNames[link.Attrs().Name]; ok {
			indices[link.Attrs().Index] = struct{}{}
		}
	}
	return indices
}

// isMirrorLoopback reports whether a link is a stale mirror loopback candidate: a
// dummy interface enslaved to a VRF that currently carries mirror config. Scoping
// to mirror VRFs ensures dummies in unrelated VRFs are never deleted.
func isMirrorLoopback(link netlink.Link, mirrorVRFIndices map[int]struct{}) bool {
	if link.Type() != "dummy" {
		return false
	}
	_, ok := mirrorVRFIndices[link.Attrs().MasterIndex]
	return ok
}

func isMirrorGREName(name string) bool {
	return strings.HasPrefix(name, greMirrorPrefix) || strings.HasPrefix(name, gretapMirrorPrefix)
}

// isMirrorSourceName reports whether a link is a possible mirror source interface:
// the VLAN bridge port (`vlan.*`, the L2 source) or the fabric VXLAN (`vx.*`, the
// VRF source). The legacy Layer2 bridge (`l2.*`) is included so that mirror filters
// left on the bridge master by older versions (which attached there) are cleaned up
// on upgrade.
func isMirrorSourceName(name string) bool {
	return strings.HasPrefix(name, vlanPrefix) ||
		strings.HasPrefix(name, vxlanPrefix) ||
		strings.HasPrefix(name, layer2SVI)
}

// MirrorSourceL2 returns the interface name used to mirror a Layer2 VLAN's traffic.
//
// The mirror attaches to the VLAN bridge port `vlan.<id>` (the workload-facing
// access port of the `l2.<vlan>` bridge), not the bridge master. Attaching to the
// port captures port-to-port bridged (east-west) traffic between the workload side
// and the L2VNI overlay (`vx.<vni>`) — which a clsact hook on the bridge master
// would miss, as port-to-port frames are switched in the bridge fast path and
// never traverse the master netdev.
func MirrorSourceL2(vlan int) string {
	return fmt.Sprintf("%s%d", vlanPrefix, vlan)
}

// MirrorSourceVRF returns the interface name used to mirror a fabric VRF's traffic
// (the VXLAN interface). The VXLAN is named by VNI.
func MirrorSourceVRF(vni uint32) string {
	return fmt.Sprintf("%s%d", vxlanPrefix, vni)
}

// prefixFamily returns the ethertype (ethPIP / ethPIPv6) of a CIDR/IP string, or
// 0 when it is empty or unparseable.
func prefixFamily(prefix string) uint16 {
	if prefix == "" {
		return 0
	}
	if strings.Contains(prefix, ":") {
		return ethPIPv6
	}
	return ethPIP
}

// parseHostOrCIDR parses a CIDR (e.g. "10.0.0.0/8") or a bare host IP (e.g.
// "76.4.0.4", treated as /32 for IPv4 or /128 for IPv6) into an IP + mask. A bare
// host would otherwise be silently dropped (CIDR-only parsing) and degrade the
// rule to match-all on that field.
func parseHostOrCIDR(s string) (net.IP, net.IPMask, bool) {
	if _, cidr, err := net.ParseCIDR(s); err == nil {
		return cidr.IP, cidr.Mask, true
	}
	if ip := net.ParseIP(s); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4, net.CIDRMask(net.IPv4len*bitsPerByte, net.IPv4len*bitsPerByte), true
		}
		return ip, net.CIDRMask(net.IPv6len*bitsPerByte, net.IPv6len*bitsPerByte), true
	}
	return nil, nil, false
}

// isPortProto reports whether the protocol supports L4 port matching.
func isPortProto(proto *vnl.IPProto) bool {
	if proto == nil {
		return false
	}
	switch *proto {
	case protoTCP, protoUDP, protoSCTP:
		return true
	default:
		return false
	}
}

func ipProtoPtr(p vnl.IPProto) *vnl.IPProto {
	return &p
}

func ipProtoNumber(proto string) *vnl.IPProto {
	var p vnl.IPProto
	switch strings.ToLower(proto) {
	case "tcp":
		p = protoTCP
	case "udp":
		p = protoUDP
	case "sctp":
		p = protoSCTP
	case "icmp":
		p = protoICMP
	case "icmpv6":
		p = protoICMPv6
	default:
		return nil
	}
	return &p
}
