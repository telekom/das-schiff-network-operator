package framework

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

type ipv6DADState int

const (
	ipv6DADReady ipv6DADState = iota
	ipv6DADTentative
	ipv6DADFailed
)

// WaitForIPv6DADComplete waits until the given IPv6 address on the specified
// interface inside a pod is no longer in "tentative" or "dadfailed" state.
// If DAD has failed, the helper temporarily disables DAD for the interface and
// re-adds the address once.
func (f *Framework) WaitForIPv6DADComplete(ctx context.Context, namespace, podName, ipv6Addr, ifName string, timeout time.Duration) error {
	target, err := parseIPv6Target(ipv6Addr)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	repaired := false

	return Poll(ctx, 2*time.Second, func() (bool, error) {
		stdout, stderr, err := f.ExecInPod(ctx, namespace, podName, "", []string{
			"ip", "-6", "addr", "show", "dev", ifName,
		})
		if err != nil {
			return false, fmt.Errorf("ip addr show failed (stderr=%s): %w", stderr, err)
		}

		cidr, state, found := parseIPv6DADState(stdout, target)
		if !found {
			return false, nil
		}
		switch state {
		case ipv6DADReady:
			return true, nil
		case ipv6DADTentative:
			return false, nil
		case ipv6DADFailed:
			if repaired {
				return false, fmt.Errorf("IPv6 DAD did not clear for %s on %s", ipv6Addr, ifName)
			}
			if err := f.resetIPv6AddressWithoutDAD(ctx, namespace, podName, cidr, ifName); err != nil {
				return false, err
			}
			repaired = true
			return false, nil
		default:
			return false, fmt.Errorf("unknown IPv6 DAD state for %s on %s", ipv6Addr, ifName)
		}
	})
}

func parseIPv6Target(ipv6Addr string) (netip.Addr, error) {
	target, err := netip.ParseAddr(ipv6Addr)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid IPv6 address %q: %w", ipv6Addr, err)
	}
	if !target.Is6() {
		return netip.Addr{}, fmt.Errorf("address %q is not IPv6", ipv6Addr)
	}
	return target, nil
}

func parseIPv6DADState(ipAddrOutput string, target netip.Addr) (string, ipv6DADState, bool) {
	for _, line := range strings.Split(ipAddrOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "inet6" {
			continue
		}
		cidr := fields[1]
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || prefix.Addr() != target {
			continue
		}
		if strings.Contains(line, "dadfailed") {
			return cidr, ipv6DADFailed, true
		}
		if strings.Contains(line, "tentative") {
			return cidr, ipv6DADTentative, true
		}
		return cidr, ipv6DADReady, true
	}
	return "", ipv6DADReady, false
}

func (f *Framework) resetIPv6AddressWithoutDAD(ctx context.Context, namespace, podName, cidr, ifName string) error {
	previous, err := f.readIPv6AcceptDAD(ctx, namespace, podName, ifName)
	if err != nil {
		return f.readdIPv6Address(ctx, namespace, podName, cidr, ifName, true)
	}

	if err := f.writeIPv6AcceptDAD(ctx, namespace, podName, ifName, "0"); err != nil {
		return f.readdIPv6Address(ctx, namespace, podName, cidr, ifName, true)
	}

	if err := f.readdIPv6Address(ctx, namespace, podName, cidr, ifName, false); err != nil {
		_ = f.writeIPv6AcceptDAD(ctx, namespace, podName, ifName, previous)
		return err
	}

	return f.writeIPv6AcceptDAD(ctx, namespace, podName, ifName, previous)
}

func (f *Framework) readIPv6AcceptDAD(ctx context.Context, namespace, podName, ifName string) (string, error) {
	stdout, stderr, err := f.ExecInPod(ctx, namespace, podName, "", []string{
		"cat", "/proc/sys/net/ipv6/conf/" + ifName + "/accept_dad",
	})
	if err != nil {
		return "", fmt.Errorf("read IPv6 DAD setting for %s failed (stderr=%s): %w", ifName, stderr, err)
	}
	return strings.TrimSpace(stdout), nil
}

func (f *Framework) writeIPv6AcceptDAD(ctx context.Context, namespace, podName, ifName, value string) error {
	_, stderr, err := f.ExecInPod(ctx, namespace, podName, "", []string{
		"sh", "-c", "printf '%s' \"$2\" > /proc/sys/net/ipv6/conf/$1/accept_dad", "set-dad", ifName, value,
	})
	if err != nil {
		return fmt.Errorf("write IPv6 DAD setting %s=%s failed (stderr=%s): %w", ifName, value, stderr, err)
	}
	return nil
}

func (f *Framework) readdIPv6Address(ctx context.Context, namespace, podName, cidr, ifName string, noDAD bool) error {
	_, stderr, err := f.ExecInPod(ctx, namespace, podName, "", []string{
		"ip", "-6", "addr", "del", cidr, "dev", ifName,
	})
	if err != nil {
		return fmt.Errorf("remove tentative IPv6 address %s from %s failed (stderr=%s): %w", cidr, ifName, stderr, err)
	}

	if noDAD {
		// iproute2 and BusyBox differ on nodad support and argument ordering.
		// Prefer disabling DAD, but keep E2E setup usable with minimal pod images.
		if err := f.addIPv6Address(ctx, namespace, podName, cidr, ifName, []string{"dev", ifName, "nodad"}); err == nil {
			return nil
		}
		if err := f.addIPv6Address(ctx, namespace, podName, cidr, ifName, []string{"nodad", "dev", ifName}); err == nil {
			return nil
		}
	}

	return f.addIPv6Address(ctx, namespace, podName, cidr, ifName, []string{"dev", ifName})
}

func (f *Framework) addIPv6Address(ctx context.Context, namespace, podName, cidr, ifName string, options []string) error {
	args := append([]string{"ip", "-6", "addr", "add", cidr}, options...)
	_, stderr, err := f.ExecInPod(ctx, namespace, podName, "", args)
	if err != nil {
		return fmt.Errorf("re-add IPv6 address %s to %s failed (stderr=%s): %w", cidr, ifName, stderr, err)
	}

	return nil
}
