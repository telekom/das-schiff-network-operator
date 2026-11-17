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
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

// TestManager_ScrapesDoNotInterleaveWithApply runs metrics scrapes and applies
// concurrently on one session and checks that no RPC lands between the reset,
// merge and commit of an apply. Run it with -race as well: without the session
// lock the detector also reports the unguarded m.running.
func TestManager_ScrapesDoNotInterleaveWithApply(t *testing.T) {
	var ops []string // guarded by the server's mu, which calls drop
	srv := startNCTestServer(t, &ncTestServer{drop: func(_ int, op string) bool {
		ops = append(ops, op)
		return false
	}})
	m := newNCTestManager(t, srv.addr)

	const applies, scrapes = 5, 20
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for range applies {
				require.NoError(t, m.ApplyConfiguration(context.Background(), &v1alpha1.NodeNetworkConfigSpec{}))
			}
		})
	}
	for range 2 {
		wg.Go(func() {
			for range scrapes {
				// The test server answers <ok/> without data, so the scrape
				// fails after its first RPC; only the RPC order matters here.
				_, _ = m.GetMetrics(context.Background())
			}
		})
	}
	wg.Wait()

	srv.mu.Lock()
	defer srv.mu.Unlock()
	require.Len(t, ops, 2*applies*3+2*scrapes)
	for i, op := range ops {
		if op == string(Replace) {
			require.Equal(t, []string{string(Replace), string(Merge), "commit"}, ops[i:i+3], "apply at RPC %d was interleaved", i)
		}
	}
}

func TestManager_GetMetricsGivesUpWhenSessionIsBusy(t *testing.T) {
	srv := startNCTestServer(t, &ncTestServer{})
	m := newNCTestManager(t, srv.addr)

	require.NoError(t, m.lock(context.Background())) // a long-running apply
	defer m.unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := m.GetMetrics(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 2*time.Second)

	sessions, _ := srv.stats()
	require.Zero(t, sessions, "a scrape that did not get the session sends nothing")
}

// TestNetconfSessionCloseRace reproduces #452: when the server closes a
// session, nemith.io/netconf v0.0.4 closes the transport in recvLoop while
// sendLoop may still write to it, which the race detector reports. The race
// is inside the library, so this package (including the session-drop tests
// in netconf_reconnect_test.go) cannot run under -race until upstream fixes
// it; CI runs it without -race. Run the reproducer explicitly with:
//
//	CRA_VSR_NETCONF_RACE_REPRO=1 go test -race -run TestNetconfSessionCloseRace ./pkg/cra-vsr/
func TestNetconfSessionCloseRace(t *testing.T) {
	if os.Getenv("CRA_VSR_NETCONF_RACE_REPRO") == "" {
		t.Skip("reproducer for #452 (race in nemith.io/netconf); set CRA_VSR_NETCONF_RACE_REPRO=1 and run with -race")
	}

	// The server drops the session on the first RPC; Send re-opens it and
	// retries, which succeeds.
	srv := startNCTestServer(t, &ncTestServer{drop: func(rpcs int, _ string) bool { return rpcs == 1 }})
	nc := newNCTestManager(t, srv.addr).nc

	require.NoError(t, nc.Commit(context.Background()))
	sessions, _ := srv.stats()
	require.Equal(t, 2, sessions)
}
