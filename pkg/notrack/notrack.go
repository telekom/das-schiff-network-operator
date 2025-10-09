package notrack

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/nltoolkit"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	nftTableName       = "network-operator"
	nftChainPrerouting = "prerouting"
	nftChainOutput     = "output"

	syncInterval = 20 * time.Second

	// payload offsets/lengths for IPv4 and transport header matches.
	ipv4SrcOffset  = 12
	ipv4DestOffset = 16
	ipv4DestLen    = 4
	transDstOffset = 2
	transDstLen    = 2

	// protocol numbers and ports.
	vxlanPort = 4789

	// bit shift used to split port into bytes.
	portShift = 8

	iptablesTimeout    = 5
	iptablesTable      = "raw"
	iptablesPrerouting = "PREROUTING"
	iptablesOutput     = "OUTPUT"
)

// ruleKind represents the kind of generated notrack rule (fib or iif).
type ruleKind string

const (
	ruleKindFib ruleKind = "fib"
	ruleKindIif ruleKind = "iif"
)

var (
	notrackLog = ctrl.Log.WithName("notrack")
	// usertag for VXLAN notrack rules.
	vxlanUserData = "nwop:vxlan"
	// precomputed big-endian bytes for VXLAN UDP port.
	vxlanPortBE = []byte{byte(vxlanPort >> portShift), byte(vxlanPort & 0xff)} // nolint:mnd

	oldIPtablesRules = regexp.MustCompile(`--comment "?nwop:\w+"?`)
)

func ipv4DstExprs(ip net.IP, reg uint32) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: reg, Base: expr.PayloadBaseNetworkHeader, Offset: ipv4DestOffset, Len: ipv4DestLen},
		&expr.Cmp{Op: expr.CmpOpEq, Register: reg, Data: ip.To4()},
	}
}

// ipv4SrcExprs matches the network-header source address (saddr) at offset 12.
func ipv4SrcExprs(ip net.IP, reg uint32) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: reg, Base: expr.PayloadBaseNetworkHeader, Offset: ipv4SrcOffset, Len: ipv4DestLen},
		&expr.Cmp{Op: expr.CmpOpEq, Register: reg, Data: ip.To4()},
	}
}

func udpDstPortExprs(port uint16, reg uint32) []expr.Any {
	p := []byte{byte(port >> portShift), byte(port & 0xff)} // nolint:mnd
	return []expr.Any{
		// ensure L4 proto is UDP.
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: reg + 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: reg + 1, Data: []byte{unix.IPPROTO_UDP}},
		// match transport dst port.
		&expr.Payload{DestRegister: reg, Base: expr.PayloadBaseTransportHeader, Offset: transDstOffset, Len: transDstLen},
		&expr.Cmp{Op: expr.CmpOpEq, Register: reg, Data: p},
	}
}

// helper to add a rule only if UserData tag not already present.
func addRuleIfMissing(c *nftables.Conn, chain *nftables.Chain, rule *nftables.Rule, userdata string) error {
	// list existing rules for chain.
	rules, err := c.GetRules(chain.Table, chain)
	if err != nil {
		return fmt.Errorf("error listing rules before add: %w", err)
	}
	for _, r := range rules {
		if r.UserData != nil && string(r.UserData) == userdata {
			// already present.
			return nil
		}
	}
	rule.UserData = []byte(userdata)
	// AddRule returns the created rule object; it does not return an error in this version.
	_ = c.AddRule(rule)
	return nil
}

// addFibNotrackRule creates and installs a fib-based notrack rule using interface-name patterns.
// iifPattern and oifPattern expect nftables-style wildcard patterns (for example: "l2.*", "def_*").
func addFibNotrackRule(c *nftables.Conn, chain *nftables.Chain, iifPattern, oifPattern, userdata string) error {
	// Build rule: iifname <pattern> ; fib daddr result oifname ; cmp oifname == <pattern> ; counter ; notrack.
	rule := &nftables.Rule{
		Table: chain.Table,
		Chain: chain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},                  // nolint:mnd // use register 1 for iifname
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(iifPattern)}, // nolint:mnd
			&expr.Fib{Register: 2, FlagDADDR: true, ResultOIFNAME: true},       // nolint:mnd // use register 2 for oifname
			&expr.Cmp{Op: expr.CmpOpEq, Register: 2, Data: []byte(oifPattern)}, // nolint:mnd
			&expr.Counter{},
			&expr.Notrack{},
		},
	}
	return addRuleIfMissing(c, chain, rule, userdata)
}

// addIifNotrackRule adds a notrack rule matching incoming interface name pattern only.
func addIifNotrackRule(c *nftables.Conn, chain *nftables.Chain, iifPattern, userdata string) error {
	rule := &nftables.Rule{
		Table: chain.Table,
		Chain: chain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},                  // nolint:mnd // use register 1 for iifname
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(iifPattern)}, // nolint:mnd
			&expr.Counter{},
			&expr.Notrack{},
		},
	}
	return addRuleIfMissing(c, chain, rule, userdata)
}

// ensureChain ensures a chain exists in the given table: it returns a Chain object and
// creates the chain in the kernel if it does not already exist. This centralizes the
// create-or-reuse logic used for IPv4 and IPv6 chains.
func ensureChain(c *nftables.Conn, table *nftables.Table, name string, ctype nftables.ChainType, hooknum *nftables.ChainHook, priority *nftables.ChainPriority) *nftables.Chain {
	chain := &nftables.Chain{
		Table:    table,
		Name:     name,
		Type:     ctype,
		Hooknum:  hooknum,
		Priority: priority,
	}
	if _, err := c.ListChain(table, name); err != nil {
		// If listing fails (chain missing or transient error), attempt to add the chain.
		// If AddChain fails, it's fine to proceed — the caller will observe errors when
		// interacting with nftables. Log for diagnostics.
		notrackLog.Info("adding nftables chain", "table", table.Name, "chain", name)
		c.AddChain(chain)
	}
	return chain
}

// reconcileLinks: ensure generic notrack rules are present once at startup and avoid duplicates.
// Now returns an error if any install operation fails.
func reconcileLinks(c *nftables.Conn, chainPrerouting4, chainPrerouting6 *nftables.Chain) error {
	var errStrings []string
	// static list of generic rules to install. Each entry describes which conn/chain to use and
	// whether to use a FIB rule (iif->oif) or simple iif match.
	type ruleDef struct {
		conn     *nftables.Conn
		kind     ruleKind // ruleKindFib or ruleKindIif
		iif      string
		oif      string // only for fib
		userdata string
	}

	rules := []ruleDef{
		// l2 -> l2
		{c, ruleKindFib, "l2.", "l2.", "nwop:gen:l2-l2"},
		// l2 -> def
		{c, ruleKindFib, "l2.", "def_", "nwop:gen:l2-def"},
		// l2 -> br
		{c, ruleKindFib, "l2.", "br.", "nwop:gen:l2-br"},
		// l2 -> dv
		{c, ruleKindFib, "l2.", "dv.", "nwop:gen:l2-dv"},
		// br -> l2
		{c, ruleKindFib, "br.", "l2.", "nwop:gen:br-l2"},
		// dv -> l2
		{c, ruleKindFib, "dv.", "l2.", "nwop:gen:dv-l2"},
		// def -> l2
		{c, ruleKindFib, "def_", "l2.", "nwop:gen:def-l2"},
		// vr
		{c, ruleKindIif, "vr.", "", "nwop:gen:vr"},
	}

	for _, rd := range rules {
		switch rd.kind {
		case ruleKindFib:
			if err := addFibNotrackRule(rd.conn, chainPrerouting4, rd.iif, rd.oif, rd.userdata); err != nil {
				notrackLog.Error(err, "failed to install generic notrack fib rule", "rule", rd.userdata)
				errStrings = append(errStrings, fmt.Sprintf("%s: %v", rd.userdata, err))
			}
			if err := addFibNotrackRule(rd.conn, chainPrerouting6, rd.iif, rd.oif, rd.userdata); err != nil {
				notrackLog.Error(err, "failed to install generic notrack fib rule", "rule", rd.userdata)
				errStrings = append(errStrings, fmt.Sprintf("%s: %v", rd.userdata, err))
			}
		case ruleKindIif:
			if err := addIifNotrackRule(rd.conn, chainPrerouting4, rd.iif, rd.userdata); err != nil {
				notrackLog.Error(err, "failed to install generic notrack iif rule", "rule", rd.userdata)
				errStrings = append(errStrings, fmt.Sprintf("%s: %v", rd.userdata, err))
			}
			if err := addIifNotrackRule(rd.conn, chainPrerouting6, rd.iif, rd.userdata); err != nil {
				notrackLog.Error(err, "failed to install generic notrack iif rule", "rule", rd.userdata)
				errStrings = append(errStrings, fmt.Sprintf("%s: %v", rd.userdata, err))
			}
		default:
			notrackLog.Info("unknown rule kind in reconcileLinks", "kind", rd.kind)
		}
	}

	// flush once after adding for both families using the single conn.
	if err := c.Flush(); err != nil {
		notrackLog.Error(err, "error flushing nftables after installing generic notrack rules")
		errStrings = append(errStrings, fmt.Sprintf("flush: %v", err))
	}

	if len(errStrings) > 0 {
		return fmt.Errorf("reconcileLinks encountered errors: %s", strings.Join(errStrings, "; "))
	}
	return nil
}

// helper to identify vxlan rules by usertag + payload matches.
func isVxlanRule(rule *nftables.Rule, underlayIP net.IP) bool {
	if rule.UserData == nil || string(rule.UserData) != vxlanUserData {
		return false
	}
	// accept rules that match either dst or src to support prerouting and output chains.
	hasDst := ruleHasIPv4Dst(rule, underlayIP)
	hasSrc := ruleHasIPv4Src(rule, underlayIP)
	hasPort := ruleHasUDPPort(rule, vxlanPortBE)
	return hasPort && (hasDst || hasSrc)
}

// ruleHasIPv4Dst returns true if the rule contains a network-header payload cmp matching underlayIP at the destination offset.
func ruleHasIPv4Dst(rule *nftables.Rule, underlayIP net.IP) bool {
	for i := 0; i < len(rule.Exprs)-1; i++ {
		if payload, ok := rule.Exprs[i].(*expr.Payload); ok && payload.Base == expr.PayloadBaseNetworkHeader && payload.Offset == ipv4DestOffset {
			if cmp, ok := rule.Exprs[i+1].(*expr.Cmp); ok && net.IP(cmp.Data).Equal(underlayIP) {
				return true
			}
		}
	}
	return false
}

// ruleHasIPv4Src returns true if the rule contains a network-header payload cmp matching underlayIP at the source offset.
func ruleHasIPv4Src(rule *nftables.Rule, underlayIP net.IP) bool {
	for i := 0; i < len(rule.Exprs)-1; i++ {
		if payload, ok := rule.Exprs[i].(*expr.Payload); ok && payload.Base == expr.PayloadBaseNetworkHeader && payload.Offset == ipv4SrcOffset {
			if cmp, ok := rule.Exprs[i+1].(*expr.Cmp); ok && net.IP(cmp.Data).Equal(underlayIP) {
				return true
			}
		}
	}
	return false
}

// ruleHasUDPPort returns true if the rule contains a transport-header payload cmp matching the given big-endian port bytes.
func ruleHasUDPPort(rule *nftables.Rule, portBE []byte) bool {
	for i := 0; i < len(rule.Exprs)-1; i++ {
		if payload, ok := rule.Exprs[i].(*expr.Payload); ok && payload.Base == expr.PayloadBaseTransportHeader {
			if cmp, ok := rule.Exprs[i+1].(*expr.Cmp); ok {
				if len(cmp.Data) == transDstLen && cmp.Data[0] == portBE[0] && cmp.Data[1] == portBE[1] {
					return true
				}
			}
		}
	}
	return false
}

// deleteStaleVxlanRules removes VXLAN-userdata rules that no longer match underlayIP from the provided rules slice.
func deleteStaleVxlanRules(c *nftables.Conn, rules []*nftables.Rule, underlayIP net.IP, chain *nftables.Chain) {
	for _, rule := range rules {
		if rule.UserData == nil || string(rule.UserData) != vxlanUserData {
			continue
		}
		// Determine whether rule should be kept based on chain semantics.
		// For output chain we require an IPv4 source match (saddr). For prerouting we require daddr.
		hasPort := ruleHasUDPPort(rule, vxlanPortBE)
		var keep bool
		if chain != nil && chain.Name == nftChainOutput {
			keep = hasPort && ruleHasIPv4Src(rule, underlayIP)
		} else {
			keep = hasPort && ruleHasIPv4Dst(rule, underlayIP)
		}
		if !keep {
			if err := c.DelRule(rule); err != nil {
				notrackLog.Error(err, "error deleting old vxlan rule")
			}
		}
	}
}

// ensureVxlanExists ensures a vxlan notrack rule exists in the chain; it checks the provided rules slice for presence and adds if missing.
func ensureVxlanExists(c *nftables.Conn, chain *nftables.Chain, rules []*nftables.Rule, underlayIP net.IP) {
	present := false
	for _, rule := range rules {
		if isVxlanRule(rule, underlayIP) {
			present = true
			break
		}
	}
	if !present {
		// Use source-based match for the output chain, destination-based otherwise.
		var exprs []expr.Any
		if chain.Name == nftChainOutput {
			exprs = append(ipv4SrcExprs(underlayIP, 1), udpDstPortExprs(vxlanPort, 2)...) // nolint:mnd
		} else {
			exprs = append(ipv4DstExprs(underlayIP, 1), udpDstPortExprs(vxlanPort, 2)...) // nolint:mnd
		}
		rx := &nftables.Rule{
			Table:    chain.Table,
			Chain:    chain,
			Exprs:    append(exprs, &expr.Counter{}, &expr.Notrack{}),
			UserData: []byte(vxlanUserData),
		}
		_ = c.AddRule(rx)
	}
}

// reconcileVxlanForUnderlay ensures VXLAN notrack rules exist for the supplied underlayIP
// and removes stale ones. This keeps the logic out of the main sync loop for readability.
func reconcileVxlanForUnderlay(underlayIP net.IP, c *nftables.Conn, chainPrerouting4, chainOutput4 *nftables.Chain) {
	// Clean up old VXLAN rules in prerouting/output (IPv4).
	preroutingRules, _ := c.GetRules(chainPrerouting4.Table, chainPrerouting4)
	outputRules, _ := c.GetRules(chainOutput4.Table, chainOutput4)
	deleteStaleVxlanRules(c, preroutingRules, underlayIP, chainPrerouting4)
	deleteStaleVxlanRules(c, outputRules, underlayIP, chainOutput4)

	// Ensure presence of VXLAN rules in prerouting and output.
	ensureVxlanExists(c, chainPrerouting4, preroutingRules, underlayIP)
	ensureVxlanExists(c, chainOutput4, outputRules, underlayIP)

	if err := c.Flush(); err != nil {
		notrackLog.Error(err, "error flushing nftables after vxlan reconciliation")
	}
}

func syncNFTables(netlinkManager *nl.Manager, c *nftables.Conn, chainPrerouting4, chainOutput4, chainPrerouting6 *nftables.Chain) {
	// Run generic link-based rules once at startup to avoid duplicates.
	if err := reconcileLinks(c, chainPrerouting4, chainPrerouting6); err != nil {
		notrackLog.Error(err, "reconcileLinks failed at startup (continuing)")
	}

	for {
		if underlayIP, err := netlinkManager.GetUnderlayIP(); err == nil {
			reconcileVxlanForUnderlay(underlayIP, c, chainPrerouting4, chainOutput4)
		} else {
			notrackLog.Error(err, "error reconciling VXLAN notrack in IPv4 nftables")
		}
		// Sleep then re-check underlay IP periodically.
		time.Sleep(syncInterval)
	}
}

func cleanupProtocol(protocol iptables.Protocol, table, chain string) error {
	ipt, err := iptables.New(iptables.IPFamily(protocol), iptables.Timeout(iptablesTimeout))
	if err != nil {
		return fmt.Errorf("error connecting to iptables for notrack cleanup: %w", err)
	}
	rules, err := ipt.List(table, chain)
	if err != nil {
		return fmt.Errorf("error listing %s rules in %s table for notrack cleanup: %w", chain, table, err)
	}
	for _, rule := range rules {
		if oldIPtablesRules.MatchString(rule) {
			// rule contains old usertag, delete it.
			if err := ipt.Delete(table, chain, strings.Fields(rule)[2:]...); err != nil {
				notrackLog.Error(err, "error deleting old iptables notrack rule (continuing)", "rule", rule)
			} else {
				notrackLog.Info("deleted old iptables notrack rule", "rule", rule)
			}
		}
	}
	return nil
}

func cleanupOldIptablesRules() error {
	if err := cleanupProtocol(iptables.ProtocolIPv4, iptablesTable, iptablesPrerouting); err != nil {
		return err
	}
	if err := cleanupProtocol(iptables.ProtocolIPv4, iptablesTable, iptablesOutput); err != nil {
		return err
	}
	if err := cleanupProtocol(iptables.ProtocolIPv6, iptablesTable, iptablesPrerouting); err != nil {
		return err
	}
	return nil
}

// Exported wrapper started by user: RunNoTrackSync.
func RunNoTrackSync() error {
	if err := cleanupOldIptablesRules(); err != nil {
		notrackLog.Error(err, "error cleaning up old iptables notrack rules (continuing)")
	}

	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("error creating nftables connection: %w", err)
	}

	table4 := &nftables.Table{Name: nftTableName, Family: nftables.TableFamilyIPv4}
	table6 := &nftables.Table{Name: nftTableName, Family: nftables.TableFamilyIPv6}

	// Ensure tables exist (idempotent): if ListTableOfFamily finds the table we reuse it, otherwise add.
	if t, err := conn.ListTableOfFamily(nftTableName, nftables.TableFamilyIPv4); err == nil {
		table4 = t
	} else {
		conn.AddTable(table4)
	}
	if t6, err := conn.ListTableOfFamily(nftTableName, nftables.TableFamilyIPv6); err == nil {
		table6 = t6
	} else {
		conn.AddTable(table6)
	}

	// create or reuse chains for IPv4 and IPv6 using helper.
	chainPrerouting4 := ensureChain(conn, table4, nftChainPrerouting, nftables.ChainTypeFilter, nftables.ChainHookPrerouting, nftables.ChainPriorityRaw)
	chainOutput4 := ensureChain(conn, table4, nftChainOutput, nftables.ChainTypeFilter, nftables.ChainHookOutput, nftables.ChainPriorityRaw)

	chainPrerouting6 := ensureChain(conn, table6, nftChainPrerouting, nftables.ChainTypeFilter, nftables.ChainHookPrerouting, nftables.ChainPriorityRaw)

	// create netlink manager.
	netlinkManager := nl.NewManager(&nltoolkit.Toolkit{})
	go syncNFTables(netlinkManager, conn, chainPrerouting4, chainOutput4, chainPrerouting6)
	return nil
}
