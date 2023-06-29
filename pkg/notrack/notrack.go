package notrack

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"github.com/vishvananda/netlink"
	"k8s.io/utils/strings/slices"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

const (
	IPTABLES_TABLE = "raw"
	IPTABLES_CHAIN = "PREROUTING"
)

var (
	notrackLog = ctrl.Log.WithName("notrack")

	rulesRegex          = regexp.MustCompile(`--comment "?nwop:notrack"?`)
	inputInterfaceRegex = regexp.MustCompile(`-i "?([a-zA-Z0-9._]*)"?`)
	notrackLinkPrefixes = []string{"vr."}
)

func parseRuleInterface(rule string) (string, error) {
	matches := inputInterfaceRegex.FindStringSubmatch(rule)
	if len(matches) != 2 {
		return "", fmt.Errorf("illegal matchcount %d (should be 2)", len(matches))
	}
	return matches[1], nil
}

func buildRule(link string) []string {
	return []string{"-i", link, "-m", "comment", "--comment", "nwop:notrack", "-j", "NOTRACK"}
}

func reconcileIPTables(notrackLinks []string, ipt *iptables.IPTables) error {
	rules, err := ipt.List(IPTABLES_TABLE, IPTABLES_CHAIN)
	if err != nil {
		return err
	}

	var existingLinks []string
	for _, rule := range rules {
		if !rulesRegex.MatchString(rule) {
			continue
		}
		link, err := parseRuleInterface(rule)
		if err != nil {
			notrackLog.Error(err, "error parsing rule", "rule", rule)
		}

		if !slices.Contains(notrackLinks, link) {
			if err := ipt.Delete(IPTABLES_TABLE, IPTABLES_CHAIN, buildRule(link)...); err != nil {
				return err
			}
		}

		existingLinks = append(existingLinks, link)
	}
	for _, notrackLink := range notrackLinks {
		if slices.Contains(existingLinks, notrackLink) {
			continue
		}
		if err := ipt.Append(IPTABLES_TABLE, IPTABLES_CHAIN, buildRule(notrackLink)...); err != nil {
			return err
		}
	}
	return nil
}

func RunIPTablesSync() {
	ipt4, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Timeout(5))
	if err != nil {
		notrackLog.Error(err, "error connecting to ip4tables for notrack")
		os.Exit(1)
	}
	ipt6, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv6), iptables.Timeout(5))
	if err != nil {
		notrackLog.Error(err, "error connecting to ip6tables for notrack")
		os.Exit(1)
	}

	netlinkManager := &nl.NetlinkManager{}

	go func() {
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
				if err := ipt4.AppendUnique(IPTABLES_TABLE, IPTABLES_CHAIN, "-d", underlayIP.String(), "-p", "udp", "--dport", "4789", "-j", "NOTRACK"); err != nil {
					notrackLog.Error(err, "error reconciling VXLAN notrack in IPv4 iptables")
				}
			} else {
				notrackLog.Error(err, "error reconciling VXLAN notrack in IPv4 iptables")
			}

			time.Sleep(20 * time.Second)
		}
	}()
}
