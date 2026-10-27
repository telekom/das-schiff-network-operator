package tests

import "os"

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
