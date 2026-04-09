package framework

import (
	"context"
	"fmt"
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

// WaitForIPv6DADComplete waits until the IPv6 address on the given interface
// is no longer in "tentative" state inside the pod. In containerlab/Kind
// environments, DAD (Duplicate Address Detection) can fail because CRA bridge
// agents on other nodes respond to DAD probes, causing the address to enter
// "dadfailed" state. This function detects that and resets the address.
func (f *Framework) WaitForIPv6DADComplete(ctx context.Context, namespace, podName, ipv6Addr, iface string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return Poll(ctx, 3*time.Second, func() (bool, error) {
		stdout, _, err := f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "-6", "addr", "show", "dev", iface})
		if err != nil {
			return false, nil
		}

		for _, line := range strings.Split(stdout, "\n") {
			if !strings.Contains(line, ipv6Addr) {
				continue
			}
			if strings.Contains(line, "dadfailed") {
				// DAD failed — reset address to re-trigger DAD.
				_, _, _ = f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "addr", "del", ipv6Addr + "/64", "dev", iface})
				time.Sleep(500 * time.Millisecond)
				_, _, _ = f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "addr", "add", ipv6Addr + "/64", "dev", iface})
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

// EnsureIPv6NoDad disables DAD on the given interface and re-adds the IPv6 address
// without DAD. In containerlab/Kind environments, CRA bridge agents
// respond to DAD probes causing addresses to permanently enter "dadfailed" state.
// Disabling DAD entirely is the correct approach for these synthetic test environments.
func (f *Framework) EnsureIPv6NoDad(ctx context.Context, namespace, podName, ipv6Addr, iface string) error {
	// Disable DAD on the interface via sysctl (best-effort, ignore errors).
	//nolint:dogsled // ExecInPod returns stdout, stderr, err — none needed for best-effort sysctl
	_, _, _ = f.ExecInPod(ctx, namespace, podName, "", []string{
		"sysctl", "-w", fmt.Sprintf("net.ipv6.conf.%s.accept_dad=0", iface),
	})

	// Remove the existing address (which may be in dadfailed/tentative state).
	//nolint:dogsled // ExecInPod returns stdout, stderr, err — none needed for best-effort addr del
	_, _, _ = f.ExecInPod(ctx, namespace, podName, "", []string{
		"ip", "addr", "del", ipv6Addr + "/64", "dev", iface,
	})

	// Re-add the address — sysctl accept_dad=0 already prevents DAD,
	// so we don't need the nodad flag (which older iproute2 versions lack).
	_, stderr, err := f.ExecInPod(ctx, namespace, podName, "", []string{
		"ip", "addr", "add", ipv6Addr + "/64", "dev", iface,
	})
	if err != nil {
		return fmt.Errorf("failed to re-add IPv6 address: %s: %w", stderr, err)
	}

	return nil
}
