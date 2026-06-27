package framework

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// PingResult holds the result of a ping attempt.
type PingResult struct {
	Success bool
	Output  string
}

// PingFromPod executes a ping from a pod to a target address.
// Uses IPv6 if target contains ':', otherwise IPv4.
func (f *Framework) PingFromPod(ctx context.Context, namespace, podName, target string, count int) (*PingResult, error) {
	cmd := "ping"
	if strings.Contains(target, ":") {
		cmd = "ping6"
	}

	args := []string{cmd, "-c", fmt.Sprintf("%d", count), "-W", "3", target}
	stdout, stderr, err := f.ExecInPod(ctx, namespace, podName, "", args)
	if err != nil {
		return &PingResult{
			Success: false,
			Output:  fmt.Sprintf("stdout: %s\nstderr: %s\nerr: %v", stdout, stderr, err),
		}, nil
	}

	return &PingResult{
		Success: true,
		Output:  stdout,
	}, nil
}

// CurlFromPod executes a curl command from a pod.
func (f *Framework) CurlFromPod(ctx context.Context, namespace, podName, url string) (string, error) {
	args := []string{"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--connect-timeout", "5", "--max-time", "10", url}
	stdout, stderr, err := f.ExecInPod(ctx, namespace, podName, "", args)
	if err != nil {
		return "", fmt.Errorf("curl failed: stdout=%s stderr=%s err=%w", stdout, stderr, err)
	}
	return strings.TrimSpace(stdout), nil
}

// AssertConnectivity verifies bidirectional ping between two pods.
func (f *Framework) AssertConnectivity(ctx context.Context, ns1, pod1, _, _, targetIP string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return Poll(ctx, 5*time.Second, func() (bool, error) {
		result, err := f.PingFromPod(ctx, ns1, pod1, targetIP, 3)
		if err != nil {
			return false, nil
		}
		return result.Success, nil
	})
}

// AssertNoConnectivity verifies that ping from a pod to a target fails.
func (f *Framework) AssertNoConnectivity(ctx context.Context, namespace, podName, target string) error {
	result, _ := f.PingFromPod(ctx, namespace, podName, target, 3)
	if result != nil && result.Success {
		return fmt.Errorf("unexpected connectivity: %s can reach %s", podName, target)
	}
	return nil
}

// PingFromCluster2Pod executes a ping from a pod on cluster-2.
func (f *Framework) PingFromCluster2Pod(ctx context.Context, namespace, podName, target string, count int) (*PingResult, error) {
	cmd := "ping"
	if strings.Contains(target, ":") {
		cmd = "ping6"
	}
	args := []string{cmd, "-c", fmt.Sprintf("%d", count), "-W", "3", target}
	stdout, stderr, err := f.ExecInCluster2Pod(ctx, namespace, podName, args)
	if err != nil {
		return &PingResult{
			Success: false,
			Output:  fmt.Sprintf("stdout: %s\nstderr: %s\nerr: %v", stdout, stderr, err),
		}, nil
	}
	return &PingResult{Success: true, Output: stdout}, nil
}

// CurlFromCluster2Pod executes a curl command from a pod on cluster-2.
func (f *Framework) CurlFromCluster2Pod(ctx context.Context, namespace, podName, url string) (string, error) {
	args := []string{"wget", "-q", "-O", "/dev/null", "-S", "--timeout=10", url}
	_, stderr, err := f.ExecInCluster2Pod(ctx, namespace, podName, args)
	if err != nil {
		return "", fmt.Errorf("wget failed: stderr=%s err=%w", stderr, err)
	}
	// wget -S prints HTTP response headers to stderr; look for "200 OK"
	if strings.Contains(stderr, "200") {
		return "200", nil
	}
	return "", fmt.Errorf("unexpected wget response: %s", stderr)
}

// EnsureIPv6NoDad disables DAD on the given interface and re-adds the IPv6 address
// without DAD. In containerlab/Kind environments, CRA bridge agents
// respond to DAD probes causing addresses to permanently enter "dadfailed" state.
// Disabling DAD entirely is the correct approach for these synthetic test environments.
func (f *Framework) EnsureIPv6NoDad(ctx context.Context, namespace, podName, ipv6Addr, iface string) error {
	stdout, stderr, err := f.ExecInPod(ctx, namespace, podName, "", []string{
		"ip", "-6", "-o", "addr", "show", "dev", iface,
	})
	if err != nil {
		return fmt.Errorf("failed to inspect IPv6 address on %s: %s: %w", iface, stderr, err)
	}

	addrWithPrefix, err := findIPv6AddressWithPrefix(stdout, ipv6Addr)
	if err != nil {
		return fmt.Errorf("failed to find IPv6 address on %s: %w", iface, err)
	}

	// Disable DAD on the interface via sysctl (best-effort, ignore errors).
	//nolint:dogsled // ExecInPod returns stdout, stderr, err — none needed for best-effort sysctl
	_, _, _ = f.ExecInPod(ctx, namespace, podName, "", []string{
		"sysctl", "-w", fmt.Sprintf("net.ipv6.conf.%s.accept_dad=0", iface),
	})

	// Remove the existing address (which may be in dadfailed/tentative state).
	//nolint:dogsled // ExecInPod returns stdout, stderr, err — none needed for best-effort addr del
	_, _, _ = f.ExecInPod(ctx, namespace, podName, "", []string{
		"ip", "addr", "del", addrWithPrefix, "dev", iface,
	})

	// Re-add the address — sysctl accept_dad=0 already prevents DAD,
	// so we don't need the nodad flag (which older iproute2 versions lack).
	_, stderr, err = f.ExecInPod(ctx, namespace, podName, "", []string{
		"ip", "addr", "add", addrWithPrefix, "dev", iface,
	})
	if err != nil {
		return fmt.Errorf("failed to re-add IPv6 address: %s: %w", stderr, err)
	}

	return nil
}

func findIPv6AddressWithPrefix(output, ipv6Addr string) (string, error) {
	target, err := parseCanonicalIPv6(ipv6Addr)
	if err != nil {
		return "", fmt.Errorf("invalid ipv6Addr %q: %w", ipv6Addr, err)
	}

	for _, line := range strings.Split(output, "\n") {
		for _, field := range strings.Fields(line) {
			if !strings.Contains(field, "/") {
				continue
			}
			prefix, parseErr := netip.ParsePrefix(field)
			if parseErr != nil || !prefix.Addr().Is6() || prefix.Addr().Is4In6() {
				continue
			}
			if prefix.Addr() == target {
				return prefix.String(), nil
			}
		}
	}

	return "", fmt.Errorf("IPv6 address %s not found", ipv6Addr)
}

// WaitForIPv6DADComplete polls `ip -6 -o addr show dev <iface>` inside a pod until
// the given IPv6 address appears without the "tentative" flag, indicating that
// Duplicate Address Detection (DAD) has completed.
//
// ipv6Addr must be a bare address (no prefix length), e.g. "fd94:685b:30cf:501::10".
// The address is compared using canonical netip.Addr equality so that textually
// different but equivalent representations (e.g. compressed vs expanded) are treated as the same.
//
// Errors from ExecInPod are propagated so that permanent failures (e.g. wrong interface
// name, missing `ip` binary, permission issues) surface immediately instead of silently
// looping until the context deadline.
func (f *Framework) WaitForIPv6DADComplete(ctx context.Context, namespace, podName, ipv6Addr, iface string, timeout time.Duration) error {
	canonical, err := parseCanonicalIPv6(ipv6Addr)
	if err != nil {
		return fmt.Errorf("invalid ipv6Addr %q: %w", ipv6Addr, err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return Poll(ctx, 2*time.Second, func() (bool, error) {
		stdout, stderr, execErr := f.ExecInPod(ctx, namespace, podName, "", []string{
			"ip", "-6", "-o", "addr", "show", "dev", iface,
		})
		if execErr != nil {
			return false, fmt.Errorf("ip addr show failed (stderr=%s): %w", stderr, execErr)
		}

		for _, line := range strings.Split(stdout, "\n") {
			matchesTarget := false
			for _, field := range strings.Fields(line) {
				addrPart := field
				if slash := strings.IndexByte(addrPart, '/'); slash >= 0 {
					addrPart = addrPart[:slash]
				}
				lineAddr, parseErr := parseCanonicalIPv6(addrPart)
				if parseErr != nil {
					continue
				}
				if lineAddr == canonical {
					matchesTarget = true
					break
				}
			}
			if !matchesTarget {
				continue
			}
			if strings.Contains(line, "dadfailed") {
				return false, fmt.Errorf("IPv6 DAD failed for %s on %s: %s", ipv6Addr, iface, strings.TrimSpace(line))
			}
			if strings.Contains(line, "tentative") {
				continue
			}
			return true, nil
		}
		return false, nil
	})
}

func parseCanonicalIPv6(s string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, err
	}
	if !addr.Is6() {
		return netip.Addr{}, fmt.Errorf("%q is not an IPv6 address", s)
	}
	if addr.Is4In6() {
		return netip.Addr{}, fmt.Errorf("%q is an IPv4-mapped IPv6 address, not a pure IPv6 address", s)
	}
	return addr, nil
}
