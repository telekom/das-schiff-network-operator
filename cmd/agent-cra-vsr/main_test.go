package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateCraManagerRejectsWhitespaceKnownHostsPath(t *testing.T) {
	t.Setenv("CRA_URL", "169.254.33.1:830")
	t.Setenv("CRA_TIMEOUT", "1s")
	t.Setenv("CRA_USER", "test-user")
	t.Setenv("CRA_PASSWORD_FILE", "/nonexistent")
	t.Setenv("CRA_KNOWN_HOSTS", "   ")

	_, err := createCraManager()
	if err == nil {
		t.Fatal("createCraManager() returned nil error, want missing CRA_KNOWN_HOSTS error")
	}
	if !strings.Contains(err.Error(), "CRA_KNOWN_HOSTS") {
		t.Fatalf("createCraManager() error = %q, want CRA_KNOWN_HOSTS context", err)
	}
}

func TestCreateCraManagerRejectsLegacyPassword(t *testing.T) {
	t.Setenv("CRA_URL", "169.254.33.1:830")
	t.Setenv("CRA_TIMEOUT", "1s")
	t.Setenv("CRA_USER", "test-user")
	t.Setenv("CRA_PASSWORD", "test-password")
	t.Setenv("CRA_PRIVATE_KEY_FILE", "")
	t.Setenv("CRA_PASSWORD_FILE", "")
	t.Setenv("CRA_KNOWN_HOSTS", "/etc/cra/known_hosts")

	_, err := createCraManager()
	if err == nil {
		t.Fatal("createCraManager() returned nil error, want CRA_PASSWORD rejection")
	}
	if !strings.Contains(err.Error(), "CRA_PASSWORD is no longer supported") ||
		!strings.Contains(err.Error(), "CRA_PASSWORD_FILE") {
		t.Fatalf("createCraManager() error = %q, want migration hint to CRA_PASSWORD_FILE", err)
	}
	if strings.Contains(err.Error(), "test-password") {
		t.Fatalf("createCraManager() error leaks the password: %q", err)
	}
}

func TestCraAuthFromEnvUsesPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRA_PRIVATE_KEY_FILE", "")
	t.Setenv("CRA_PASSWORD_FILE", path)

	if _, err := craAuthFromEnv(); err != nil {
		t.Fatalf("craAuthFromEnv() error = %v", err)
	}
}
