package main

import (
	"strings"
	"testing"
)

func TestCreateCraManagerRejectsWhitespaceKnownHostsPath(t *testing.T) {
	t.Setenv("CRA_URL", "169.254.33.1:830")
	t.Setenv("CRA_TIMEOUT", "1s")
	t.Setenv("CRA_USER", "test-user")
	t.Setenv("CRA_PASSWORD", "test-password")
	t.Setenv("CRA_KNOWN_HOSTS", "   ")

	_, err := createCraManager()
	if err == nil {
		t.Fatal("createCraManager() returned nil error, want missing CRA_KNOWN_HOSTS error")
	}
	if !strings.Contains(err.Error(), "CRA_KNOWN_HOSTS") {
		t.Fatalf("createCraManager() error = %q, want CRA_KNOWN_HOSTS context", err)
	}
}
