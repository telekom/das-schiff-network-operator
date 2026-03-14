// Package config provides embedded test manifests.
package config

import (
	"os"
	"path/filepath"
)

// ReadManifest reads a manifest file from the testdata directory.
// It resolves the path relative to the repository root.
func ReadManifest(path string) ([]byte, error) {
	root := os.Getenv("REPO_ROOT")
	if root != "" {
		return os.ReadFile(filepath.Join(root, "e2etests", "testdata", path))
	}
	// go test sets cwd to the package directory (e2etests/)
	return os.ReadFile(filepath.Join("testdata", path))
}
