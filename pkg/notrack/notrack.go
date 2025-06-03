package notrack

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/vishvananda/netlink"
	"k8s.io/utils/strings/slices"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	iptablesTable      = "raw"
	iptablesPrerouting = "PREROUTING"
	iptablesOutput     = "OUTPUT"

	iptablesTimeout  = 5
	syncInterval     = 20 * time.Second
	ruleMatchesCount = 2
)

var (
	notrackLog = ctrl.Log.WithName("notrack")

	vrfRulesRegex       = regexp.MustCompile(`--comment "?nwop:notrack"?`)
	l2RulesRegex        = regexp.MustCompile(`--comment "?nwop:l2"?`)
	inputInterfaceRegex = regexp.MustCompile(`-i "?([a-zA-Z0-9._-]*)"?`)
	dstRegex            = regexp.MustCompile(`-d "?([0-9a-fA-F.:/]+)"?`)
	notrackLinkPrefixes = []string{"vr."}
	l2InterfacePrefixes = []string{"l2."}
)

type RuleParser func(string) (string, error)
type RuleBuilder func(string) []string

func parseRuleInterface(rule string) (string, error) {
	matches := inputInterfaceRegex.FindStringSubmatch(rule)
	if len(matches) != ruleMatchesCount {
		return "", fmt.Errorf("illegal matchcount %d (should be %d)", len(matches), ruleMatchesCount)
	}
	return matches[1], nil
}

func parseRuleDestination(rule string) (string, error) {
	matches := dstRegex.FindStringSubmatch(rule)
	if len(matches) != ruleMatchesCount {
		return "", fmt.Errorf("illegal matchcount %d (should be %d)", len(matches), ruleMatchesCount)
	}
	return matches[1], nil
}

func buildVrfRule(link string) []string {
	return []string{"-i", link, "-m", "comment", "--comment", "nwop:notrack", "-j", "NOTRACK"}
}

func buildL2Rule(dst string) []string {
	return []string{"-d", dst, "-m", "comment", "--comment", "nwop:l2", "-j", "NOTRACK"}
}

func reconcileRules(regex *regexp.Regexp, parameters, rules []string, parser RuleParser, builder RuleBuilder, ipt *iptables.IPTables) error {
	var existing []string

	for _, rule := range rules {
		if regex.MatchString(rule) {
			link, err := parser(rule)
			if err != nil {
				notrackLog.Error(err, "error parsing rule", "rule", rule)
			}

			if !slices.Contains(parameters, link) {
				if err := ipt.Delete(iptablesTable, iptablesPrerouting, builder(link)...); err != nil {
					return fmt.Errorf("error deleting IPTables rule: %w", err)
				}
			}

			existing = append(existing, link)
		}
	}

	for _, param := range parameters {
		if slices.Contains(existing, param) {
			continue
		}
		if err := ipt.Append(iptablesTable, iptablesPrerouting, builder(param)...); err != nil {
			return fmt.Errorf("error appending IPTables rule: %w", err)
		}
	}
	return nil
}

func reconcileIPTables(notrackLinks []string, l2Destinations []string, ipt *iptables.IPTables) error {
	rules, err := ipt.List(iptablesTable, iptablesPrerouting)
	if err != nil {
		return fmt.Errorf("error listing IPTables rules: %w", err)
	}

	if err := reconcileRules(vrfRulesRegex, notrackLinks, rules, parseRuleInterface, buildVrfRule, ipt); err != nil {
		return fmt.Errorf("error reconciling VRF rules: %w", err)
	}

	if err := reconcileRules(l2RulesRegex, l2Destinations, rules, parseRuleDestination, buildL2Rule, ipt); err != nil {
		return fmt.Errorf("error reconciling L2 rules: %w", err)
	}

	return nil
}

func RunIPTablesSync() error {
	ipt4, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Timeout(iptablesTimeout))
	if err != nil {
		return fmt.Errorf("error connecting to ip4tables for notrack: %w", err)
	}
	ipt6, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv6), iptables.Timeout(iptablesTimeout))
	if err != nil {
		return fmt.Errorf("error connecting to ip6tables for notrack: %w", err)
	}

	netlinkManager := nl.NewManager(&nl.Toolkit{})

	go syncIPTTables(netlinkManager, ipt4, ipt6)

	return nil
}

func appendDestinations(link netlink.Link, family int, destinations []string) []string {
	addresses, err := netlink.AddrList(link, family)
	if err != nil {
		notrackLog.Error(err, "error getting addresses for link", "link", link.Attrs().Name)
		return destinations
	}

	for _, addr := range addresses {
		if addr.IP.IsGlobalUnicast() {
			destinations = append(destinations, addr.IP.Mask(addr.Mask).String())
		}
	}
	return destinations
}

func reconcileLinks(ipt4, ipt6 *iptables.IPTables) {
	links, err := netlink.LinkList()
	if err != nil {
		notrackLog.Error(err, "error getting link list for notrack check")
		return
	}

	var notrackLinks []string
	var v4Destinations []string
	var v6Destinations []string

	for _, link := range links {
		// If the link is a VRF interface, we need to add it to the notrack rules
		for _, notrackPrefix := range notrackLinkPrefixes {
			if !strings.HasPrefix(link.Attrs().Name, notrackPrefix) {
				continue
			}

			notrackLinks = append(notrackLinks, link.Attrs().Name)
			break
		}

		// If the link is an L2 interface, we need to add its addresses to the notrack rules
		for _, l2InterfacePrefix := range l2InterfacePrefixes {
			if !strings.HasPrefix(link.Attrs().Name, l2InterfacePrefix) {
				continue
			}

			v4Destinations = appendDestinations(link, netlink.FAMILY_V4, v4Destinations)
			v6Destinations = appendDestinations(link, netlink.FAMILY_V6, v6Destinations)
			break
		}
	}

	if err := reconcileIPTables(notrackLinks, v4Destinations, ipt4); err != nil {
		notrackLog.Error(err, "error reconciling notrack in IPv4 iptables")
	}
	if err := reconcileIPTables(notrackLinks, v6Destinations, ipt6); err != nil {
		notrackLog.Error(err, "error reconciling notrack in IPv6 iptables")
	}
}

func syncIPTTables(netlinkManager *nl.Manager, ipt4, ipt6 *iptables.IPTables) {
	for {
		reconcileLinks(ipt4, ipt6)

		if underlayIP, err := netlinkManager.GetUnderlayIP(); err == nil {
			if err := ipt4.AppendUnique(iptablesTable, iptablesPrerouting, "-d", underlayIP.String(), "-p", "udp", "--dport", "4789", "-j", "NOTRACK"); err != nil {
				notrackLog.Error(err, "error reconciling VXLAN notrack in IPv4 iptables")
			}
			if err := ipt4.AppendUnique(iptablesTable, iptablesOutput, "-s", underlayIP.String(), "-p", "udp", "--dport", "4789", "-j", "NOTRACK"); err != nil {
				notrackLog.Error(err, "error reconciling VXLAN notrack in IPv4 iptables")
			}
		} else {
			notrackLog.Error(err, "error reconciling VXLAN notrack in IPv4 iptables")
		}

		time.Sleep(syncInterval)
	}
}
