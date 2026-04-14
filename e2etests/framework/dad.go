package framework

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// WaitForIPv6DADComplete waits until the given IPv6 address on the specified
// interface inside a pod is no longer in "tentative" state, meaning DAD
// (Duplicate Address Detection) has finished.
func (f *Framework) WaitForIPv6DADComplete(ctx context.Context, namespace, podName, ipv6Addr, ifName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return Poll(ctx, 2*time.Second, func() (bool, error) {
		stdout, stderr, err := f.ExecInPod(ctx, namespace, podName, "", []string{
			"ip", "-6", "addr", "show", "dev", ifName,
		})
		if err != nil {
			return false, fmt.Errorf("ip addr show failed (stderr=%s): %w", stderr, err)
		}
		for _, line := range strings.Split(stdout, "\n") {
			if !strings.Contains(line, ipv6Addr) {
				continue
			}
			if strings.Contains(line, "dadfailed") {
				return false, fmt.Errorf("IPv6 DAD failed for %s on %s", ipv6Addr, ifName)
			}
			if strings.Contains(line, "tentative") {
				return false, nil
			}
			return true, nil
		}
		return false, nil
	})
}
