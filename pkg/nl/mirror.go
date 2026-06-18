//nolint:wrapcheck
package nl

import (
	"fmt"
	"net"
	"strings"

	"github.com/vishvananda/netlink"
	vnl "github.com/vishvananda/netlink/nl"
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

	ethPAll  = uint16(0x0003) // ETH_P_ALL
	ethPIP   = uint16(0x0800) // ETH_P_IP
	ethPIPv6 = uint16(0x86DD) // ETH_P_IPV6

	greKeyFlag = uint16(0x2000) // GRE_KEY present flag

	protoTCP    = 6
	protoUDP    = 17
	protoICMP   = 1
	protoICMPv6 = 58
)

// ReconcileMirror programs the full mirror data path described by the
// NetlinkConfiguration: per-VRF loopbacks, GRE tunnels and tc mirror filters.
// Stale mirror resources that are no longer desired are removed.
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

	if err := n.CleanupMirrors(cfg.GRETunnels, cfg.Mirrors); err != nil {
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

	link, err := n.toolkit.LinkByName(cfg.Name)
	if err != nil {
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

	for _, addrStr := range cfg.Addresses {
		addr, err := n.toolkit.ParseAddr(addrStr)
		if err != nil {
			return fmt.Errorf("error parsing address %s: %w", addrStr, err)
		}
		if err := n.toolkit.AddrAdd(link, addr); err != nil && !strings.Contains(err.Error(), "exists") {
			return fmt.Errorf("error adding address %s to %s: %w", addrStr, cfg.Name, err)
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

	if link, err := n.toolkit.LinkByName(t.Name); err == nil {
		return link.Attrs().Index, nil
	}

	localIP := net.ParseIP(t.Local)
	remoteIP := net.ParseIP(t.Remote)
	if localIP == nil || remoteIP == nil {
		return 0, fmt.Errorf("invalid GRE IPs: local=%s remote=%s", t.Local, t.Remote)
	}

	vrfLink, err := n.toolkit.LinkByName(t.VRF)
	if err != nil {
		return 0, fmt.Errorf("VRF %q not found for GRE tunnel: %w", t.VRF, err)
	}

	if err := n.toolkit.LinkAdd(greLink(t, vrfLink.Attrs().Index, localIP, remoteIP)); err != nil {
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

func greLink(t *GRETunnel, masterIndex int, local, remote net.IP) netlink.Link {
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
	if err := n.clearMirrorFilters(link); err != nil {
		return err
	}

	for i := range rules {
		r := &rules[i]
		greIdx, ok := tunnelIndex[r.GREInterface]
		if !ok {
			return fmt.Errorf("no GRE tunnel index for interface %s", r.GREInterface)
		}
		if mirrorFilterPriorityBase+i > int(^uint16(0)) {
			return fmt.Errorf("too many mirror rules on %s: priority overflow at index %d", iface, i)
		}
		prio := uint16(mirrorFilterPriorityBase + i) //nolint:gosec // bounds-checked above
		if r.Direction == string(directionIngress) || r.Direction == directionBoth {
			if err := n.addMirrorFilter(link, handleMinIngress, prio, r, greIdx); err != nil {
				return fmt.Errorf("error adding ingress filter: %w", err)
			}
		}
		if r.Direction == string(directionEgress) || r.Direction == directionBoth {
			if err := n.addMirrorFilter(link, handleMinEgress, prio, r, greIdx); err != nil {
				return fmt.Errorf("error adding egress filter: %w", err)
			}
		}
	}
	return nil
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
func (n *Manager) clearMirrorFilters(link netlink.Link) error {
	for _, parent := range []uint32{handleMinIngress, handleMinEgress} {
		filters, err := n.toolkit.FilterList(link, parent)
		if err != nil {
			continue
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

func (n *Manager) addMirrorFilter(link netlink.Link, parent uint32, prio uint16, rule *MirrorRule, greIfIndex int) error {
	flower := &netlink.Flower{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: link.Attrs().Index,
			Parent:    parent,
			Priority:  prio,
			Protocol:  ethPAll,
		},
		Actions: []netlink.Action{
			&netlink.MirredAction{
				ActionAttrs:  netlink.ActionAttrs{Action: netlink.TC_ACT_PIPE},
				MirredAction: netlink.TCA_EGRESS_MIRROR,
				Ifindex:      greIfIndex,
			},
		},
	}

	if rule.Protocol != "" {
		if proto := ipProtoNumber(rule.Protocol); proto != nil {
			flower.IPProto = proto
		}
	}
	if rule.SrcPrefix != "" {
		if _, cidr, err := net.ParseCIDR(rule.SrcPrefix); err == nil {
			flower.SrcIP = cidr.IP
			flower.SrcIPMask = cidr.Mask
		}
	}
	if rule.DstPrefix != "" {
		if _, cidr, err := net.ParseCIDR(rule.DstPrefix); err == nil {
			flower.DestIP = cidr.IP
			flower.DestIPMask = cidr.Mask
		}
	}
	if rule.SrcPort > 0 {
		flower.SrcPort = rule.SrcPort
	}
	if rule.DstPort > 0 {
		flower.DestPort = rule.DstPort
	}
	if flower.IPProto != nil || flower.SrcIP != nil || flower.DestIP != nil {
		if isIPv6(rule.SrcPrefix, rule.DstPrefix) {
			flower.EthType = ethPIPv6
		} else {
			flower.EthType = ethPIP
		}
	}

	if err := n.toolkit.FilterAdd(flower); err != nil {
		return fmt.Errorf("error adding flower filter: %w", err)
	}
	return nil
}

// CleanupMirrors removes stale GRE tunnels and stale mirror filters that are no
// longer in the desired state. GRE tunnels are identified by the mirror naming
// prefixes. Only mirror filters (in the dedicated mirror priority range) are
// removed from mirror-shaped source interfaces (l2.*, vx.*) that are no longer
// referenced by any rule; the shared clsact qdisc and the node's forwarding/BPF
// filters are never touched.
func (n *Manager) CleanupMirrors(tunnels []GRETunnel, rules []MirrorRule) error {
	links, err := n.toolkit.LinkList()
	if err != nil {
		return fmt.Errorf("error listing links: %w", err)
	}

	desiredGRE := make(map[string]struct{}, len(tunnels))
	for i := range tunnels {
		desiredGRE[tunnels[i].Name] = struct{}{}
	}
	desiredSources := make(map[string]struct{}, len(rules))
	for i := range rules {
		desiredSources[rules[i].SourceInterface] = struct{}{}
	}

	for _, link := range links {
		name := link.Attrs().Name
		if isMirrorGREName(name) {
			if _, ok := desiredGRE[name]; !ok {
				if err := n.toolkit.LinkDel(link); err != nil {
					return fmt.Errorf("error deleting stale GRE %s: %w", name, err)
				}
			}
			continue
		}
		if isMirrorSourceName(name) {
			if _, ok := desiredSources[name]; !ok {
				if err := n.clearMirrorFilters(link); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func isMirrorGREName(name string) bool {
	return strings.HasPrefix(name, greMirrorPrefix) || strings.HasPrefix(name, gretapMirrorPrefix)
}

func isMirrorSourceName(name string) bool {
	return strings.HasPrefix(name, layer2SVI) || strings.HasPrefix(name, vxlanPrefix)
}

// MirrorSourceL2 returns the interface name used to mirror a Layer2 VLAN's traffic
// (the Layer2 bridge).
func MirrorSourceL2(vlan int) string {
	return fmt.Sprintf("%s%d", layer2SVI, vlan)
}

// MirrorSourceVRF returns the interface name used to mirror a fabric VRF's traffic
// (the VXLAN interface).
func MirrorSourceVRF(vrf string) string {
	return vxlanPrefix + vrf
}

func isIPv6(prefixes ...string) bool {
	for _, p := range prefixes {
		if p != "" && strings.Contains(p, ":") {
			return true
		}
	}
	return false
}

func ipProtoNumber(proto string) *vnl.IPProto {
	var p vnl.IPProto
	switch strings.ToLower(proto) {
	case "tcp":
		p = protoTCP
	case "udp":
		p = protoUDP
	case "icmp":
		p = protoICMP
	case "icmpv6":
		p = protoICMPv6
	default:
		return nil
	}
	return &p
}
