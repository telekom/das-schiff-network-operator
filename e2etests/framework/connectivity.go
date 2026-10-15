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

// ipv6CIDR returns the configured address and its actual prefix from iproute2.
// The prefix is part of the CNI-assigned address and must not be guessed.
func ipv6CIDR(stdout, ipv6Addr string) (string, bool) {
	target, err := netip.ParseAddr(ipv6Addr)
	if err != nil || !target.Is6() || target.Is4In6() {
		return "", false
	}
	for _, line := range strings.Split(stdout, "\n") {
		for _, field := range strings.Fields(line) {
			prefix, err := netip.ParsePrefix(field)
			if err == nil && prefix.Addr() == target {
				return prefix.String(), true
			}
		}
	}
	return "", false
}

// WaitForIPv6DADComplete waits until the IPv6 address on the given interface
// is no longer tentative inside the pod. A dadfailed address is reset using
// the prefix reported by iproute2, preserving non-/64 CNI configurations.
func (f *Framework) WaitForIPv6DADComplete(ctx context.Context, namespace, podName, ipv6Addr, iface string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return Poll(ctx, 3*time.Second, func() (bool, error) {
		stdout, _, err := f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "-6", "-o", "addr", "show", "dev", iface})
		if err != nil {
			return false, nil
		}
		cidr, found := ipv6CIDR(stdout, ipv6Addr)
		if !found {
			return false, nil
		}
		for _, line := range strings.Split(stdout, "\n") {
			if !strings.Contains(line, cidr) {
				continue
			}
			if strings.Contains(line, "dadfailed") {
				_, _, _ = f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "addr", "del", cidr, "dev", iface})
				time.Sleep(500 * time.Millisecond)
				_, _, _ = f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "addr", "add", cidr, "dev", iface})
				return false, nil
			}
			if strings.Contains(line, "tentative") {
				return false, nil
			}
			return true, nil
		}
		return false, nil
	})
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

// EnsureIPv6NoDad disables DAD on the given interface and re-adds the IPv6
// address with the prefix currently assigned by CNI.
func (f *Framework) EnsureIPv6NoDad(ctx context.Context, namespace, podName, ipv6Addr, iface string) error {
	stdout, stderr, err := f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "-6", "-o", "addr", "show", "dev", iface})
	if err != nil {
		return fmt.Errorf("inspect IPv6 address: stderr=%s: %w", stderr, err)
	}
	cidr, found := ipv6CIDR(stdout, ipv6Addr)
	if !found {
		return fmt.Errorf("IPv6 address %s not found on %s", ipv6Addr, iface)
	}
	_, stderr, sysctlErr := f.ExecInPod(ctx, namespace, podName, "", []string{
		"sysctl", "-w", fmt.Sprintf("net.ipv6.conf.%s.accept_dad=0", iface),
	})
	if sysctlErr != nil {
		// CNI tuning can set this before the pod starts, while the pod's /proc/sys
		// remains read-only. Accept that already-safe state.
		value, readStderr, readErr := f.ExecInPod(ctx, namespace, podName, "", []string{
			"sysctl", "-n", fmt.Sprintf("net.ipv6.conf.%s.accept_dad", iface),
		})
		if readErr != nil || strings.TrimSpace(value) != "0" {
			if readErr != nil {
				stderr = fmt.Sprintf("%s; read current value: %s", stderr, readStderr)
			}
			return fmt.Errorf("disable IPv6 DAD on %s: stderr=%s: %w", iface, stderr, sysctlErr)
		}
	}
	if _, stderr, err = f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "addr", "del", cidr, "dev", iface}); err != nil {
		return fmt.Errorf("remove IPv6 address %s: stderr=%s: %w", cidr, stderr, err)
	}
	if _, stderr, err = f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "addr", "add", cidr, "dev", iface}); err != nil {
		return fmt.Errorf("re-add IPv6 address %s: stderr=%s: %w", cidr, stderr, err)
	}
	return nil
}
