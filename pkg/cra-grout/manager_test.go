package cra

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewManagerRejectsMalformedCert guards the failure mode that has no useful
// symptom: a cert file that parses as no certificates leaves the trust pool
// empty, and every request then fails at the handshake with an "unknown
// authority" that says nothing about the real cause.
func TestNewManagerRejectsMalformedCert(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, []byte("this is not a PEM certificate\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("nor is this a key\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := NewManager([]string{"https://localhost:8080"}, time.Second, certPath, keyPath); err == nil {
		t.Error("NewManager accepted a cert file containing no certificates")
	}
}

func TestNewManagerRejectsMissingCert(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewManager([]string{"https://localhost:8080"}, time.Second,
		filepath.Join(dir, "absent.pem"), filepath.Join(dir, "absent.key")); err == nil {
		t.Error("NewManager accepted a missing cert file")
	}
}
