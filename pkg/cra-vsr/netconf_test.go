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
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

			err := validateKnownHostsEntries(path, tt.urls)
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
