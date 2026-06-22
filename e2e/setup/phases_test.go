package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetGoVersionReadsGoDirective(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.test\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	version, err := getGoVersion(repoRoot)
	if err != nil {
		t.Fatalf("getGoVersion returned error: %v", err)
	}
	if version != "1.26.0" {
		t.Fatalf("version = %q, want 1.26.0", version)
	}
}

func TestGetGoVersionErrorsWhenGoModMissing(t *testing.T) {
	_, err := getGoVersion(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing go.mod")
	}
}

func TestGetGoVersionErrorsWhenDirectiveMissing(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := getGoVersion(repoRoot)
	if err == nil {
		t.Fatal("expected error for missing go directive")
	}
}
