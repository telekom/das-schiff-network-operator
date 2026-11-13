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

// Package netconf runs the CRA-VSR NETCONF client against OpenSSH in a
// container (e2e/images/netconf-ssh). Run it with `make e2e-netconf`.
package netconf

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	cra "github.com/telekom/das-schiff-network-operator/pkg/cra-vsr"
)

const (
	image    = "das-schiff-netconf-ssh:e2e"
	user     = "netconf"
	password = "netconf-e2e-password" //nolint:gosec // throw-away password of the test container
	timeout  = 10 * time.Second
)

// endpoint is one sshd instance of the container (see entrypoint.sh).
type endpoint struct {
	addr    string
	hostKey ssh.PublicKey
}

type server struct {
	defaultAlgs endpoint // 830: OpenSSH defaults, ed25519 host key
	weakKex     endpoint // 831: only diffie-hellman-group14-sha1
	rsa2048     endpoint // 832: 2048-bit RSA host key
}

func TestMain(m *testing.M) {
	if os.Getenv("E2E_NETCONF") == "" {
		// Needs docker; run via `make e2e-netconf`.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func startServer(t *testing.T) *server {
	t.Helper()

	docker(t, "build", "-q", "-t", image, filepath.Join("..", "..", "e2e", "images", "netconf-ssh"))
	id := docker(t, "run", "-d", "--rm",
		"-e", "NETCONF_PASSWORD="+password,
		"-p", "127.0.0.1::830", "-p", "127.0.0.1::831", "-p", "127.0.0.1::832",
		image)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", id).CombinedOutput()
			t.Logf("sshd logs:\n%s", out)
		}
		_ = exec.Command("docker", "rm", "-f", id).Run()
	})

	ep := func(port, hostKeyFile string) endpoint {
		addr := docker(t, "port", id, port)
		var key ssh.PublicKey
		deadline := time.Now().Add(30 * time.Second)
		for {
			out, err := exec.Command("docker", "exec", id, "cat", hostKeyFile).Output()
			if err == nil {
				if key, _, _, _, err = ssh.ParseAuthorizedKey(out); err != nil {
					t.Fatalf("parse %s: %v", hostKeyFile, err)
				}
				if sshBannerReadable(addr) {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("sshd on %s not ready", port)
			}
			time.Sleep(500 * time.Millisecond)
		}
		return endpoint{addr: addr, hostKey: key}
	}

	return &server{
		defaultAlgs: ep("830", "/etc/ssh/host_ed25519.pub"),
		weakKex:     ep("831", "/etc/ssh/host_ed25519.pub"),
		rsa2048:     ep("832", "/etc/ssh/host_rsa2048.pub"),
	}
}

func knownHostsFile(t *testing.T, addr string, key ssh.PublicKey) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{addr}, key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func open(t *testing.T, addr string, knownHostKey ssh.PublicKey) error {
	t.Helper()
	nc, err := cra.NewNetconfWithKnownHosts([]string{addr}, user, password, knownHostsFile(t, addr, knownHostKey), timeout, cra.SSHOptions{})
	if err != nil {
		t.Fatalf("NewNetconfWithKnownHosts: %v", err)
	}
	return nc.Open(context.Background())
}

func TestNetconfClientAgainstOpenSSH(t *testing.T) {
	srv := startServer(t)

	t.Run("verified host key and password connects", func(t *testing.T) {
		if err := open(t, srv.defaultAlgs.addr, srv.defaultAlgs.hostKey); err != nil {
			t.Fatalf("Open: %v", err)
		}
	})

	t.Run("host key mismatch is rejected", func(t *testing.T) {
		other, _, _, _, err := ssh.ParseAuthorizedKey([]byte(
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8g"))
		if err != nil {
			t.Fatal(err)
		}
		err = open(t, srv.defaultAlgs.addr, other)
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			t.Fatalf("Open error = %v, want knownhosts.KeyError", err)
		}
	})

	t.Run("weak key exchange is rejected", func(t *testing.T) {
		err := open(t, srv.weakKex.addr, srv.weakKex.hostKey)
		if err == nil || !strings.Contains(err.Error(), "no common algorithm for key exchange") {
			t.Fatalf("Open error = %v, want key exchange failure", err)
		}
	})

	t.Run("2048-bit RSA host key is rejected", func(t *testing.T) {
		err := open(t, srv.rsa2048.addr, srv.rsa2048.hostKey)
		if err == nil || !strings.Contains(err.Error(), "RSA key has 2048 bits") {
			t.Fatalf("Open error = %v, want RSA key size rejection", err)
		}
	})
}

// sshBannerReadable reports whether sshd answers on addr. A plain TCP connect
// is not enough: the Docker port proxy accepts before sshd listens.
func sshBannerReadable(addr string) bool {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	banner := make([]byte, len("SSH-2.0-"))
	_, err = io.ReadFull(conn, banner)
	return err == nil && string(banner) == "SSH-2.0-"
}
