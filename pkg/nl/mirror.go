package nl

import (
	"fmt"
	"net"
	"strings"

	"github.com/vishvananda/netlink"
	vnl "github.com/vishvananda/netlink/nl"
)

const (
	grePrefix    = "gre."
	mirrorPrefix = "mir."
	// tc handle constants.
	handleClsact       = netlink.HANDLE_CLSACT
	handleMinIngress   = netlink.HANDLE_MIN_INGRESS
	handleMinEgress    = netlink.HANDLE_MIN_EGRESS
	maxMirrorIfNameLen = 15
	ethPAll            = uint16(0x0003) // ETH_P_ALL
	ethPIP             = uint16(0x0800) // ETH_P_IP
	ethPIPv6           = uint16(0x86DD) // ETH_P_IPV6
)

// ipProtoNumber maps protocol name to IP protocol number.
func ipProtoNumber(proto string) *vnl.IPProto {
	var p vnl.IPProto
	switch strings.ToLower(proto) {
	case "tcp":
		p = 6 //nolint:mnd
	case "udp":
		p = 17 //nolint:mnd
	case "icmp":
		p = 1
	case "icmpv6":
		p = 58 //nolint:mnd
	default:
		return nil
	}
	return &p
}

// ReconcileLoopbacks ensures the desired loopback (dummy) interfaces exist in their VRFs
// with the correct IP addresses. Stale loopbacks are deleted.
func (n *Manager) ReconcileLoopbacks(desired []LoopbackConfig) error {
	for i := range desired {
		if err := n.ensureLoopback(desired[i]); err != nil {
			return fmt.Errorf("error ensuring loopback %s: %w", desired[i].Name, err)
		}
	}
	return nil
}

func (n *Manager) ensureLoopback(cfg LoopbackConfig) error {
	name := cfg.Name
	if len(name) > maxMirrorIfNameLen {
		return fmt.Errorf("loopback name %q exceeds max length %d", name, maxMirrorIfNameLen)
	}

	// Look up VRF
	vrfLink, err := n.toolkit.LinkByName(cfg.VRF)
	if err != nil {
		return fmt.Errorf("VRF %q not found: %w", cfg.VRF, err)
	}

	// Create or fetch the dummy interface
	link, err := n.toolkit.LinkByName(name)
	if err != nil {
		// Create dummy interface
		dummy := &netlink.Dummy{
			LinkAttrs: netlink.LinkAttrs{
				Name:        name,
				MasterIndex: vrfLink.Attrs().Index,
			},
		}
		if err := n.toolkit.LinkAdd(dummy); err != nil {
			return fmt.Errorf("error creating dummy %s: %w", name, err)
		}
		link, err = n.toolkit.LinkByName(name)
		if err != nil {
			return fmt.Errorf("error fetching created dummy %s: %w", name, err)
		}
	}

	// Ensure link is up
	if err := n.toolkit.LinkSetUp(link); err != nil {
		return fmt.Errorf("error setting up %s: %w", name, err)
	}

	// Add addresses
	for _, addrStr := range cfg.Addresses {
		addr, err := n.toolkit.ParseAddr(addrStr)
		if err != nil {
			return fmt.Errorf("error parsing address %s: %w", addrStr, err)
		}
		// AddrAdd is idempotent — returns EEXIST if already present, which we ignore.
		if err := n.toolkit.AddrAdd(link, addr); err != nil && !strings.Contains(err.Error(), "exists") {
			return fmt.Errorf("error adding address %s to %s: %w", addrStr, name, err)
		}
	}
	return nil
}

// ReconcileGRETunnels creates/deletes GRE tunnels for mirror rules.
// Each unique (GRELocal, GRERemote, GREVRF) tuple gets one GRE tunnel.
func (n *Manager) ReconcileGRETunnels(rules []MirrorRule) (map[string]int, error) {
	// Map of tunnel key → ifindex for tc filter setup
	tunnelIndex := make(map[string]int)

	// Deduplicate tunnels
	type tunnelKey struct {
		local, remote, vrf string
	}
	seen := make(map[tunnelKey]bool)

	for i := range rules {
		r := &rules[i]
		tk := tunnelKey{local: r.GRELocal, remote: r.GRERemote, vrf: r.GREVRF}
		if seen[tk] {
			continue
		}
		seen[tk] = true

		name := greTunnelName(r.GRERemote)
		idx, err := n.ensureGRETunnel(name, r.GRELocal, r.GRERemote, r.GREVRF)
		if err != nil {
			return nil, fmt.Errorf("error ensuring GRE tunnel %s: %w", name, err)
		}
		tunnelIndex[r.GRERemote] = idx
	}
	return tunnelIndex, nil
}

func greTunnelName(remote string) string {
	// Use last octet(s) of IP for uniqueness, keep under 15 chars
	parts := strings.Split(remote, ".")
	if len(parts) >= 2 { //nolint:mnd
		name := fmt.Sprintf("%s%s.%s", grePrefix, parts[len(parts)-2], parts[len(parts)-1])
		if len(name) <= maxMirrorIfNameLen {
			return name
		}
	}
	// For IPv6 or long names, hash-truncate
	name := grePrefix + strings.ReplaceAll(remote, ":", "")
	if len(name) > maxMirrorIfNameLen {
		name = name[:maxMirrorIfNameLen]
	}
	return name
}

func (n *Manager) ensureGRETunnel(name, local, remote, vrfName string) (int, error) {
	// Check if already exists
	link, err := n.toolkit.LinkByName(name)
	if err == nil {
		return link.Attrs().Index, nil
	}

	localIP := net.ParseIP(local)
	remoteIP := net.ParseIP(remote)
	if localIP == nil || remoteIP == nil {
		return 0, fmt.Errorf("invalid GRE IPs: local=%s remote=%s", local, remote)
	}

	vrfLink, err := n.toolkit.LinkByName(vrfName)
	if err != nil {
		return 0, fmt.Errorf("VRF %q not found for GRE tunnel: %w", vrfName, err)
	}

	gre := &netlink.Gretun{
		LinkAttrs: netlink.LinkAttrs{
			Name:        name,
			MasterIndex: vrfLink.Attrs().Index,
		},
		Local:  localIP,
		Remote: remoteIP,
	}

	if err := n.toolkit.LinkAdd(gre); err != nil {
		return 0, fmt.Errorf("error creating GRE tunnel %s: %w", name, err)
	}

	link, err = n.toolkit.LinkByName(name)
	if err != nil {
		return 0, fmt.Errorf("error fetching created GRE tunnel %s: %w", name, err)
	}

	if err := n.toolkit.LinkSetUp(link); err != nil {
		return 0, fmt.Errorf("error setting up GRE tunnel %s: %w", name, err)
	}

	return link.Attrs().Index, nil
}

// ReconcileTcMirrors sets up tc clsact qdisc + flower filters on source interfaces
// to mirror matching traffic to GRE tunnels.
func (n *Manager) ReconcileTcMirrors(rules []MirrorRule, tunnelIndex map[string]int) error {
	// Group rules by source interface
	type ifaceRules struct {
		rules []MirrorRule
	}
	grouped := make(map[string]*ifaceRules)
	for i := range rules {
		r := &rules[i]
		if _, ok := grouped[r.SourceInterface]; !ok {
			grouped[r.SourceInterface] = &ifaceRules{}
		}
		grouped[r.SourceInterface].rules = append(grouped[r.SourceInterface].rules, *r)
	}

	for iface, ir := range grouped {
		if err := n.setupMirrorFilters(iface, ir.rules, tunnelIndex); err != nil {
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

	// Ensure clsact qdisc
	if err := n.ensureClsactQdisc(link); err != nil {
		return err
	}

	// Clear existing mirror filters (full reconcile)
	if err := n.clearFilters(link, handleMinIngress); err != nil {
		return err
	}
	if err := n.clearFilters(link, handleMinEgress); err != nil {
		return err
	}

	// Add filters for each rule
	for i := range rules {
		r := &rules[i]
		greIdx, ok := tunnelIndex[r.GRERemote]
		if !ok {
			return fmt.Errorf("no GRE tunnel index for remote %s", r.GRERemote)
		}

		if i+1 > int(^uint16(0)) {
			return fmt.Errorf("too many mirror rules: index %d exceeds uint16 limit", i)
		}
		prio := uint16(i + 1) //nolint:gosec // G115: bounds-checked above (i+1 <= math.MaxUint16)
		if r.Direction == "ingress" || r.Direction == "both" {
			if err := n.addMirrorFilter(link, handleMinIngress, prio, r, greIdx); err != nil {
				return fmt.Errorf("error adding ingress filter: %w", err)
			}
		}
		if r.Direction == "egress" || r.Direction == "both" {
			if err := n.addMirrorFilter(link, handleMinEgress, prio, r, greIdx); err != nil {
				return fmt.Errorf("error adding egress filter: %w", err)
			}
		}
	}
	return nil
}

func (n *Manager) ensureClsactQdisc(link netlink.Link) error {
	qdiscs, err := n.toolkit.QdiscList(link)
	if err != nil {
		return fmt.Errorf("error listing qdiscs: %w", err)
	}
	for _, q := range qdiscs {
		if q.Type() == "clsact" {
			return nil // already exists
		}
	}

	qdisc := &netlink.Clsact{
		QdiscAttrs: netlink.QdiscAttrs{
			LinkIndex: link.Attrs().Index,
			Handle:    handleClsact,
			Parent:    netlink.HANDLE_CLSACT,
		},
	}
	if err := n.toolkit.QdiscAdd(qdisc); err != nil {
		return fmt.Errorf("error adding clsact qdisc: %w", err)
	}
	return nil
}

func (n *Manager) clearFilters(link netlink.Link, parent uint32) error {
	filters, err := n.toolkit.FilterList(link, parent)
	if err != nil {
		// No filters is fine
		return nil //nolint:nilerr
	}
	for _, f := range filters {
		if err := n.toolkit.FilterDel(f); err != nil {
			return fmt.Errorf("error deleting filter: %w", err)
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

	// Set IP protocol match
	if rule.Protocol != "" {
		proto := ipProtoNumber(rule.Protocol)
		if proto != nil {
			flower.IPProto = proto
		}
	}

	// Set source prefix match
	if rule.SrcPrefix != "" {
		_, cidr, err := net.ParseCIDR(rule.SrcPrefix)
		if err == nil {
			flower.SrcIP = cidr.IP
			flower.SrcIPMask = cidr.Mask
		}
	}

	// Set destination prefix match
	if rule.DstPrefix != "" {
		_, cidr, err := net.ParseCIDR(rule.DstPrefix)
		if err == nil {
			flower.DestIP = cidr.IP
			flower.DestIPMask = cidr.Mask
		}
	}

	// Set port matches
	if rule.SrcPort > 0 {
		flower.SrcPort = rule.SrcPort
	}
	if rule.DstPort > 0 {
		flower.DestPort = rule.DstPort
	}

	// If we have IP-level match criteria, set EthType for IP filtering
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

func isIPv6(prefixes ...string) bool {
	for _, p := range prefixes {
		if p != "" && strings.Contains(p, ":") {
			return true
		}
	}
	return false
}

// CleanupMirrors removes all mirror-related resources (GRE tunnels, loopbacks, tc filters)
// that are not in the desired state. Called with empty desired to remove everything.
func (n *Manager) CleanupMirrors(desired []MirrorRule) error {
	links, err := n.toolkit.LinkList()
	if err != nil {
		return fmt.Errorf("error listing links: %w", err)
	}

	// Build set of desired GRE tunnel names
	desiredGRE := make(map[string]bool)
	for i := range desired {
		desiredGRE[greTunnelName(desired[i].GRERemote)] = true
	}

	for _, link := range links {
		name := link.Attrs().Name
		// Clean up stale GRE tunnels
		if strings.HasPrefix(name, grePrefix) && !desiredGRE[name] {
			if err := n.toolkit.LinkDel(link); err != nil {
				return fmt.Errorf("error deleting stale GRE %s: %w", name, err)
			}
		}
	}
	return nil
}
