package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFRRConfigChanged(t *testing.T) {
	dir := t.TempDir()
	orig := frrConfigPath
	frrConfigPath = filepath.Join(dir, "frr.conf")
	defer func() { frrConfigPath = orig }()

	// Missing file counts as changed.
	changed, err := frrConfigChanged("router bgp 65000\n")
	if err != nil {
		t.Fatalf("frrConfigChanged(missing) error: %v", err)
	}
	if !changed {
		t.Error("frrConfigChanged(missing file) = false, want true")
	}

	if err := os.WriteFile(frrConfigPath, []byte("router bgp 65000\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Identical content is unchanged.
	changed, err = frrConfigChanged("router bgp 65000\n")
	if err != nil {
		t.Fatalf("frrConfigChanged(same) error: %v", err)
	}
	if changed {
		t.Error("frrConfigChanged(identical) = true, want false")
	}

	// Different content is changed.
	changed, err = frrConfigChanged("router bgp 65001\n")
	if err != nil {
		t.Fatalf("frrConfigChanged(diff) error: %v", err)
	}
	if !changed {
		t.Error("frrConfigChanged(different) = false, want true")
	}
}

func TestIsGrcliExistsError(t *testing.T) {
	tolerated := []string{
		"iface add: File exists",
		"error: address already exists",
		"EEXIST",
		"Object exists on iface br2000",
	}
	for _, out := range tolerated {
		if !isGrcliExistsError([]byte(out)) {
			t.Errorf("isGrcliExistsError(%q) = false, want true", out)
		}
	}

	fatal := []string{
		"iface add: No such device",
		"error: invalid argument",
		"grcli: connection refused",
		"",
	}
	for _, out := range fatal {
		if isGrcliExistsError([]byte(out)) {
			t.Errorf("isGrcliExistsError(%q) = true, want false", out)
		}
	}
}
