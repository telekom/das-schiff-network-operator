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

	rulesRegex          = regexp.MustCompile(`--comment "?nwop:notrack"?`)
	inputInterfaceRegex = regexp.MustCompile(`-i "?([a-zA-Z0-9._]*)"?`)
	notrackLinkPrefixes = []string{"vr."}
)

func parseRuleInterface(rule string) (string, error) {
	matches := inputInterfaceRegex.FindStringSubmatch(rule)
	if len(matches) != ruleMatchesCount {
		return "", fmt.Errorf("illegal matchcount %d (should be %d)", len(matches), ruleMatchesCount)
	}
	return matches[1], nil
}

func buildRule(link string) []string {
	return []string{"-i", link, "-m", "comment", "--comment", "nwop:notrack", "-j", "NOTRACK"}
}

func reconcileIPTables(notrackLinks []string, ipt *iptables.IPTables) error {
	rules, err := ipt.List(iptablesTable, iptablesPrerouting)
	if err != nil {
		return fmt.Errorf("error listing IPTables rules: %w", err)
	}

	existingLinks := []string{}
	for _, rule := range rules {
		if !rulesRegex.MatchString(rule) {
			continue
		}
		link, err := parseRuleInterface(rule)
		if err != nil {
			notrackLog.Error(err, "error parsing rule", "rule", rule)
		}

		if !slices.Contains(notrackLinks, link) {
			if err := ipt.Delete(iptablesTable, iptablesPrerouting, buildRule(link)...); err != nil {
				return fmt.Errorf("error deleting IPTables rule: %w", err)
			}
		}

		existingLinks = append(existingLinks, link)
	}
	for _, notrackLink := range notrackLinks {
		if slices.Contains(existingLinks, notrackLink) {
			continue
		}
		if err := ipt.Append(iptablesTable, iptablesPrerouting, buildRule(notrackLink)...); err != nil {
			return fmt.Errorf("error appending IPTables rule: %w", err)
		}
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

	netlinkManager := &nl.NetlinkManager{}

	go syncIPTTables(netlinkManager, ipt4, ipt6)

	return nil
}

func syncIPTTables(netlinkManager *nl.NetlinkManager, ipt4, ipt6 *iptables.IPTables) {
	for {
		links, err := netlink.LinkList()
		if err != nil {
			notrackLog.Error(err, "error getting link list for notrack check")
			continue
		}

		var notrackLinks []string
		for _, link := range links {
			for _, notrackPrefix := range notrackLinkPrefixes {
				if strings.HasPrefix(link.Attrs().Name, notrackPrefix) {
					notrackLinks = append(notrackLinks, link.Attrs().Name)
					break
				}
			}
		}

		if err := reconcileIPTables(notrackLinks, ipt4); err != nil {
			notrackLog.Error(err, "error reconciling notrack in IPv4 iptables")
		}
		if err := reconcileIPTables(notrackLinks, ipt6); err != nil {
			notrackLog.Error(err, "error reconciling notrack in IPv6 iptables")
		}

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
