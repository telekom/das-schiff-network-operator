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

func TestCraSSHOptionsFromEnv(t *testing.T) {
	t.Setenv("CRA_SSH_MIN_RSA_HOST_KEY_BITS", "2048")
	t.Setenv("CRA_SSH_LEGACY_ALGORITHMS", "true")
	opts, err := craSSHOptionsFromEnv()
	if err != nil {
		t.Fatalf("craSSHOptionsFromEnv() error = %v", err)
	}
	if opts.MinRSAHostKeyBits != 2048 || !opts.LegacyAlgorithms {
		t.Fatalf("craSSHOptionsFromEnv() = %+v, want 2048 bits and legacy algorithms", opts)
	}

	for env, value := range map[string]string{
		"CRA_SSH_MIN_RSA_HOST_KEY_BITS": "-1",
		"CRA_SSH_LEGACY_ALGORITHMS":     "maybe",
	} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, value)
			if _, err := craSSHOptionsFromEnv(); err == nil || !strings.Contains(err.Error(), env) {
				t.Fatalf("craSSHOptionsFromEnv() error = %v, want error naming %s", err, env)
			}
		})
	}
}
