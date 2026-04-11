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
func (f *Framework) WaitForIPv6DADComplete(ctx context.Context, namespace, podName, iface, ipv6Addr string, timeout time.Duration) error {
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
			if strings.Contains(line, "tentative") {
				continue
			}
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
					if strings.Contains(line, "dadfailed") {
						return false, fmt.Errorf("DAD failed for address %s on interface %s", ipv6Addr, iface)
					}
					return true, nil
				}
			}
		}
		return false, nil
	})
}

func parseCanonicalIPv6(s string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, err
	}
	if !addr.Is6() || addr.Is4In6() {
		return netip.Addr{}, fmt.Errorf("%q is not a true IPv6 address (IPv4-mapped addresses are not accepted)", s)
	}
	return addr, nil
}
