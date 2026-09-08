package tests

import (
	"context"
	"os"
	"time"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// readTestdata reads a file from the testdata directory.
// When running inside the tester container, testdata is at /e2etests/testdata/.
// When running locally via `go test ./e2etests/`, CWD is e2etests/ so testdata/ is relative.
func readTestdata(path string) ([]byte, error) {
	// Try container path first
	data, err := os.ReadFile("/e2etests/testdata/" + path)
	if err == nil {
		return data, nil
	}
	// Fall back to relative path (CWD = e2etests/ when running go test)
	return os.ReadFile("testdata/" + path)
}

func waitForNet1IPv6Ready(ctx context.Context, f *framework.Framework, namespace, podName, ipv6Addr string, timeout time.Duration) error {
	return f.WaitForIPv6DADComplete(ctx, namespace, podName, ipv6Addr, "net1", timeout)
}
