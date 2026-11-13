/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cra

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestValidateKnownHostsEntries(t *testing.T) {
	hostKey := newTestPublicKey(t)

	tests := []struct {
		name       string
		lines      []string
		urls       []string
		wantErrSub string
	}{
		{
			name:  "single CRA endpoint",
			lines: []string{knownhosts.Line([]string{"[169.254.33.1]:830"}, hostKey)},
			urls:  []string{"169.254.33.1:830"},
		},
		{
			name: "multiple CRA endpoints",
			lines: []string{
				knownhosts.Line([]string{"[169.254.33.1]:830"}, hostKey),
				knownhosts.Line([]string{"[169.254.33.2]:830"}, hostKey),
			},
			urls: []string{"169.254.33.1:830", "169.254.33.2:830"},
		},
		{
			name:  "marker line",
			lines: []string{"@cert-authority " + knownhosts.Line([]string{"[169.254.33.1]:830"}, hostKey)},
			urls:  []string{"169.254.33.1:830"},
		},
		{
			name:  "hashed host",
			lines: []string{knownHostsLineForPattern(knownhosts.HashHostname("[169.254.33.1]:830"), hostKey)},
			urls:  []string{"169.254.33.1:830"},
		},
		{
			name:  "wildcard host",
			lines: []string{knownHostsLineForPattern("[*.example.com]:830", hostKey)},
			urls:  []string{"cra.example.com:830"},
		},
		{
			name:       "missing host",
			lines:      []string{knownhosts.Line([]string{"[169.254.33.2]:830"}, hostKey)},
			urls:       []string{"169.254.33.1:830"},
			wantErrSub: "does not contain CRA URL entries: [169.254.33.1]:830",
		},
		{
			name:       "wrong port",
			lines:      []string{knownhosts.Line([]string{"169.254.33.1"}, hostKey)},
			urls:       []string{"169.254.33.1:830"},
			wantErrSub: "does not contain CRA URL entries: [169.254.33.1]:830",
		},
		{
			name:       "missing port",
			lines:      []string{knownhosts.Line([]string{"169.254.33.1"}, hostKey)},
			urls:       []string{"169.254.33.1"},
			wantErrSub: `CRA URL "169.254.33.1" must be in host:port form`,
		},
		{
			name:       "empty file",
			urls:       []string{"169.254.33.1:830"},
			wantErrSub: "does not contain CRA URL entries: [169.254.33.1]:830",
		},
		{
			name:       "empty URLs",
			lines:      []string{knownhosts.Line([]string{"[169.254.33.1]:830"}, hostKey)},
			urls:       []string{"", " "},
			wantErrSub: "no CRA URLs provided",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "known_hosts")
			if err := os.WriteFile(path, []byte(strings.Join(tt.lines, "\n")), 0o600); err != nil {
				t.Fatalf("write known_hosts: %v", err)
			}

			_, err := validateKnownHostsEntries(path, tt.urls)
			if tt.wantErrSub == "" {
				if err != nil {
					t.Fatalf("validateKnownHostsEntries returned error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErrSub)
			}
			if !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErrSub)
			}
		})
	}
}

func TestNewNetconf_NormalizesCRAURLs(t *testing.T) {
	hostKey := newTestPublicKey(t)
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"[169.254.33.1]:830"}, hostKey)), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	nc, err := NewNetconfWithKnownHosts([]string{" 169.254.33.1:830 ", ""}, "user", "password", path, 0)
	if err != nil {
		t.Fatalf("NewNetconfWithKnownHosts returned error: %v", err)
	}
	if len(nc.urls) != 1 || nc.urls[0] != "169.254.33.1:830" {
		t.Fatalf("urls = %#v, want normalized CRA URL", nc.urls)
	}
}

func TestNewNetconfWithKnownHosts_RejectsMissingKnownHostsPath(t *testing.T) {
	_, err := NewNetconfWithKnownHosts([]string{"169.254.33.1:830"}, "user", "password", " ", 0)
	if err == nil {
		t.Fatal("NewNetconfWithKnownHosts returned nil error, want missing known hosts path error")
	}
	if !strings.Contains(err.Error(), "known hosts file path is required") {
		t.Fatalf("NewNetconfWithKnownHosts error = %q, want known hosts path context", err)
	}
}

func TestNewManagerWithKnownHosts_ValidatesBeforeDeviceAccess(t *testing.T) {
	_, err := NewManagerWithKnownHosts(
		[]string{"169.254.33.1:830"},
		"user",
		"password",
		" ",
		time.Second,
		logr.Discard(),
	)
	if err == nil {
		t.Fatal("NewManagerWithKnownHosts returned nil error, want known hosts validation error")
	}
	if !strings.Contains(err.Error(), "known hosts file path is required") {
		t.Fatalf("NewManagerWithKnownHosts error = %q, want known hosts path context", err)
	}
}

func newTestPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()

	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate test SSH key: %v", err)
	}
	sshKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("convert test SSH key: %v", err)
	}

	return sshKey
}

func knownHostsLineForPattern(pattern string, key ssh.PublicKey) string {
	return pattern + " " + key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
}

const (
	testNetconfUser     = "netconf"
	testNetconfPassword = "netconf-password"
	testNetconfHello    = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities>` +
		`<capability>urn:ietf:params:netconf:base:1.0</capability></capabilities>` +
		`<session-id>1</session-id></hello>]]>]]>`
)

// testSSHServer is an in-process SSH server that offers the NETCONF subsystem
// and counts every authentication request it receives.
type testSSHServer struct {
	addr         string
	authRequests atomic.Int32
}

func startTestSSHServer(t *testing.T, hostKey ssh.Signer, configure func(*ssh.ServerConfig)) *testSSHServer {
	t.Helper()

	srv := &testSSHServer{}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if c.User() == testNetconfUser && string(password) == testNetconfPassword {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("password rejected")
		},
		AuthLogCallback: func(ssh.ConnMetadata, string, error) {
			srv.authRequests.Add(1)
		},
	}
	cfg.AddHostKey(hostKey)
	if configure != nil {
		configure(cfg)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv.addr = ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveTestNetconf(conn, cfg)
		}
	}()

	return srv
}

func serveTestNetconf(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		ch, chReqs, err := newChannel.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range chReqs {
				isNetconf := req.Type == "subsystem"
				_ = req.Reply(isNetconf, nil)
				if isNetconf {
					_, _ = io.WriteString(ch, testNetconfHello)
					go func() { _, _ = io.Copy(io.Discard, ch) }()
				}
			}
		}()
	}
}

func newTestNetconf(t *testing.T, addr string, knownHostKey ssh.PublicKey) *Netconf {
	t.Helper()

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{addr}, knownHostKey)+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	nc, err := NewNetconfWithKnownHosts([]string{addr}, testNetconfUser, testNetconfPassword, path, 5*time.Second)
	if err != nil {
		t.Fatalf("NewNetconfWithKnownHosts: %v", err)
	}
	return nc
}

func newTestSigner(t *testing.T, key any) ssh.Signer {
	t.Helper()

	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	return signer
}

func newTestED25519Signer(t *testing.T) ssh.Signer {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	return newTestSigner(t, key)
}

func TestNetconfOpen_MatchingHostKeyConnects(t *testing.T) {
	hostKey := newTestED25519Signer(t)
	srv := startTestSSHServer(t, hostKey, nil)

	nc := newTestNetconf(t, srv.addr, hostKey.PublicKey())
	if err := nc.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

func TestNetconfOpen_RejectsBeforeAuthentication(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	tests := []struct {
		name         string
		hostKey      ssh.Signer
		knownHostKey func(server ssh.Signer) ssh.PublicKey
		configure    func(*ssh.ServerConfig)
		check        func(t *testing.T, err error)
	}{
		{
			name:         "other host key",
			hostKey:      newTestED25519Signer(t),
			knownHostKey: func(ssh.Signer) ssh.PublicKey { return newTestPublicKey(t) },
			check: func(t *testing.T, err error) {
				t.Helper()
				var keyErr *knownhosts.KeyError
				if !errors.As(err, &keyErr) {
					t.Fatalf("error = %v, want knownhosts.KeyError", err)
				}
			},
		},
		{
			name:         "only diffie-hellman-group14-sha1",
			hostKey:      newTestED25519Signer(t),
			knownHostKey: func(s ssh.Signer) ssh.PublicKey { return s.PublicKey() },
			configure: func(cfg *ssh.ServerConfig) {
				cfg.KeyExchanges = []string{ssh.InsecureKeyExchangeDH14SHA1}
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if !strings.Contains(err.Error(), "no common algorithm for key exchange") {
					t.Fatalf("error = %v, want key exchange negotiation failure", err)
				}
			},
		},
		{
			name:         "2048-bit RSA host key",
			hostKey:      newTestSigner(t, rsaKey),
			knownHostKey: func(s ssh.Signer) ssh.PublicKey { return s.PublicKey() },
			check: func(t *testing.T, err error) {
				t.Helper()
				if !strings.Contains(err.Error(), "RSA key has 2048 bits, need at least 3072") {
					t.Fatalf("error = %v, want RSA key size rejection", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := startTestSSHServer(t, tt.hostKey, tt.configure)
			nc := newTestNetconf(t, srv.addr, tt.knownHostKey(tt.hostKey))

			err := nc.Open(context.Background())
			if err == nil {
				t.Fatal("Open returned nil error")
			}
			tt.check(t, err)
			if n := srv.authRequests.Load(); n != 0 {
				t.Fatalf("server saw %d authentication requests, want 0", n)
			}
		})
	}
}

func TestBuildNetconf_RestrictsAlgorithms(t *testing.T) {
	nc := buildNetconf([]string{"169.254.33.1:830"}, "user", "password", time.Second, ssh.InsecureIgnoreHostKey())

	want := map[string][]string{
		"KeyExchanges": {"curve25519-sha256", "ecdh-sha2-nistp521", "ecdh-sha2-nistp384", "ecdh-sha2-nistp256", "diffie-hellman-group16-sha512"},
		"MACs":         {"hmac-sha2-512-etm@openssh.com", "hmac-sha2-256-etm@openssh.com", "hmac-sha2-512", "hmac-sha2-256"},
		"HostKeyAlgorithms": {
			"ssh-ed25519", "ecdsa-sha2-nistp521", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp256", "rsa-sha2-512", "rsa-sha2-256",
		},
	}
	got := map[string][]string{
		"KeyExchanges":      nc.sshConfig.KeyExchanges,
		"MACs":              nc.sshConfig.MACs,
		"HostKeyAlgorithms": nc.sshConfig.HostKeyAlgorithms,
	}
	for field, w := range want {
		if strings.Join(got[field], ",") != strings.Join(w, ",") {
			t.Errorf("%s = %v, want %v", field, got[field], w)
		}
	}
}
