package framework

import (
	"context"
	"fmt"
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
// If the address is already stuck in DAD, the helper disables DAD for the
// interface and re-adds the address once.
func (f *Framework) WaitForIPv6DADComplete(ctx context.Context, namespace, podName, ipv6Addr, ifName string, timeout time.Duration) error {
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

		cidr, state, found := parseIPv6DADState(stdout, ipv6Addr)
		if !found {
			return false, nil
		}
		switch state {
		case ipv6DADReady:
			return true, nil
		case ipv6DADTentative, ipv6DADFailed:
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

func parseIPv6DADState(ipAddrOutput, ipv6Addr string) (string, ipv6DADState, bool) {
	for _, line := range strings.Split(ipAddrOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "inet6" {
			continue
		}
		cidr := fields[1]
		if !strings.HasPrefix(cidr, ipv6Addr+"/") {
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
	_, stderr, err := f.ExecInPod(ctx, namespace, podName, "", []string{
		"sh", "-c", "printf 0 > /proc/sys/net/ipv6/conf/$1/accept_dad", "disable-dad", ifName,
	})
	if err != nil {
		return fmt.Errorf("disable IPv6 DAD for %s failed (stderr=%s): %w", ifName, stderr, err)
	}

	_, stderr, err = f.ExecInPod(ctx, namespace, podName, "", []string{
		"ip", "-6", "addr", "del", cidr, "dev", ifName,
	})
	if err != nil {
		return fmt.Errorf("remove tentative IPv6 address %s from %s failed (stderr=%s): %w", cidr, ifName, stderr, err)
	}

	_, stderr, err = f.ExecInPod(ctx, namespace, podName, "", []string{
		"ip", "-6", "addr", "add", cidr, "dev", ifName,
	})
	if err != nil {
		return fmt.Errorf("re-add IPv6 address %s to %s failed (stderr=%s): %w", cidr, ifName, stderr, err)
	}

	return nil
}
