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
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

const ncEOM = "]]>]]>"

var ncMessageID = regexp.MustCompile(`message-id="([^"]*)"`)

// ncTestServer is an in-process NETCONF-over-SSH server (base:1.0 framing). Each
// session has its own candidate, the worst case for a client that re-opens
// its session: edits of a closed session are lost. Commits record the edits
// that the committing session's candidate held.
type ncTestServer struct {
	addr string
	// idle closes a session that received no RPC for this long (0: never).
	idle time.Duration
	// drop is called before an RPC is answered; true closes the session
	// instead of answering, as a server that drops it at that moment.
	drop func(rpcs int, op string) bool

	mu       sync.Mutex
	rpcs     int
	sessions int
	commits  [][]string
}

func startNCTestServer(t *testing.T, srv *ncTestServer) *ncTestServer {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	srv.addr = ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(conn, cfg)
		}
	}()
	return srv
}

func (s *ncTestServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	// One channel per connection, as the NETCONF client opens it.
	newChannel, ok := <-chans
	if !ok {
		return
	}
	ch, chReqs, err := newChannel.Accept()
	if err != nil {
		return
	}
	req, ok := <-chReqs
	if !ok {
		return
	}
	_ = req.Reply(req.Type == "subsystem", nil)
	go ssh.DiscardRequests(chReqs)

	s.mu.Lock()
	s.sessions++
	id := s.sessions
	s.mu.Unlock()

	var idle *time.Timer
	if s.idle > 0 {
		idle = time.AfterFunc(s.idle, func() { _ = sconn.Close() })
	}
	s.session(ch, id, idle)
}

func (s *ncTestServer) session(ch ssh.Channel, id int, idle *time.Timer) {
	fmt.Fprintf(ch, `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities>`+
		`<capability>urn:ietf:params:netconf:base:1.0</capability></capabilities>`+
		`<session-id>%d</session-id></hello>%s`, id, ncEOM)

	var candidate []string
	scanner := bufio.NewScanner(ch)
	scanner.Buffer(nil, 1<<20)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.Index(data, []byte(ncEOM)); i >= 0 {
			return i + len(ncEOM), data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return 0, nil, fmt.Errorf("unterminated message")
		}
		return 0, nil, nil
	})
	for scanner.Scan() {
		msg := scanner.Text()
		if !strings.Contains(msg, "<rpc") {
			continue // client hello
		}
		if idle != nil {
			idle.Reset(s.idle)
		}

		op := "other"
		switch {
		case strings.Contains(msg, "<commit"):
			op = "commit"
		case strings.Contains(msg, "<default-operation>replace<"):
			op = string(Replace)
		case strings.Contains(msg, "<default-operation>merge<"):
			op = string(Merge)
		}

		s.mu.Lock()
		s.rpcs++
		drop := s.drop != nil && s.drop(s.rpcs, op)
		if !drop {
			switch op {
			case string(Replace):
				candidate = []string{string(Replace)}
			case string(Merge):
				candidate = append(candidate, string(Merge))
			case "commit":
				s.commits = append(s.commits, append([]string(nil), candidate...))
			}
		}
		s.mu.Unlock()
		if drop {
			_ = ch.Close()
			return
		}

		var msgID string
		if m := ncMessageID.FindStringSubmatch(msg); m != nil {
			msgID = m[1]
		}
		fmt.Fprintf(ch, `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="%s"><ok/></rpc-reply>%s`,
			msgID, ncEOM)
	}
}

func (s *ncTestServer) stats() (sessions int, commits [][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions, s.commits
}

func newNCTestManager(t *testing.T, addr string) *Manager {
	t.Helper()
	m := *manager
	m.logger = logr.Discard()
	m.startupXML = []byte(`<vrouter xmlns="urn:6wind:vrouter"/>`)
	m.running = m.startup
	m.nc = &Netconf{
		urls:    []string{addr},
		timeout: 5 * time.Second,
		sshConfig: &ssh.ClientConfig{
			User:            "netconf",
			Auth:            []ssh.AuthMethod{ssh.Password("netconf")},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // test server with a random key
		},
	}
	return &m
}

func TestNetconfSend_ReopensAfterIdleClose(t *testing.T) {
	srv := startNCTestServer(t, &ncTestServer{idle: 500 * time.Millisecond})
	nc := newNCTestManager(t, srv.addr).nc
	ctx := context.Background()

	require.NoError(t, nc.Commit(ctx))
	time.Sleep(time.Second) // the server closes the idle session
	require.NoError(t, nc.Commit(ctx))

	sessions, commits := srv.stats()
	require.Equal(t, 2, sessions)
	require.Len(t, commits, 2)
	require.Equal(t, uint64(2), nc.opens)
}

func TestApplyConfiguration_SessionDrop(t *testing.T) {
	full := []string{string(Replace), string(Merge)}
	for _, tt := range []struct {
		name     string
		drop     func(rpcs int, op string) bool
		sessions int
		commits  [][]string
	}{
		{
			name:     "no drop",
			sessions: 1,
			commits:  [][]string{full},
		},
		{
			// The merge is retried in a new session whose candidate lacks
			// the replace; that candidate must not be committed.
			name:     "drop between replace and merge",
			drop:     func(rpcs int, _ string) bool { return rpcs == 2 },
			sessions: 2,
			commits:  [][]string{full},
		},
		{
			// The commit is never retried in a new session; the whole
			// sequence runs again there.
			name:     "drop between merge and commit",
			drop:     func(rpcs int, _ string) bool { return rpcs == 3 },
			sessions: 2,
			commits:  [][]string{full},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := startNCTestServer(t, &ncTestServer{drop: tt.drop})
			m := newNCTestManager(t, srv.addr)

			require.NoError(t, m.ApplyConfiguration(context.Background(), &v1alpha1.NodeNetworkConfigSpec{}))

			sessions, commits := srv.stats()
			require.Equal(t, tt.sessions, sessions)
			require.Equal(t, tt.commits, commits)
			for _, commit := range commits {
				require.Equal(t, full, commit, "every commit holds the full sequence")
			}
		})
	}
}

func TestApplyConfiguration_GivesUpWhenEveryAttemptIsDropped(t *testing.T) {
	// Every first merge attempt is dropped; Send's retry then succeeds in a
	// new session, so each apply attempt sees a re-opened session.
	merges := 0
	srv := startNCTestServer(t, &ncTestServer{drop: func(_ int, op string) bool {
		if op != string(Merge) {
			return false
		}
		merges++
		return merges%2 == 1
	}})
	m := newNCTestManager(t, srv.addr)

	err := m.ApplyConfiguration(context.Background(), &v1alpha1.NodeNetworkConfigSpec{})
	require.ErrorContains(t, err, "re-opened during each of 3 apply attempts")

	sessions, commits := srv.stats()
	require.Equal(t, maxApplyAttempts+1, sessions)
	require.Empty(t, commits, "an incomplete candidate is never committed")
}

func TestApplyConfiguration_FailsWhenRetryFails(t *testing.T) {
	srv := startNCTestServer(t, &ncTestServer{drop: func(_ int, op string) bool { return op == string(Merge) }})
	m := newNCTestManager(t, srv.addr)

	err := m.ApplyConfiguration(context.Background(), &v1alpha1.NodeNetworkConfigSpec{})
	require.ErrorContains(t, err, "all netconf request sending attempts failed")

	_, commits := srv.stats()
	require.Empty(t, commits)
}
