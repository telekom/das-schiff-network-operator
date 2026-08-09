package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// The tolerated set is what makes a desired-state replay idempotent, so a
// marker that is too broad silently turns a real failure into a no-op.
func TestIsGrcliExistsErrorRejectsUnrelatedFailures(t *testing.T) {
	for _, out := range []string{
		"iface add: interface does not exist",
		"iface add: port exists in another domain",
		"route add: exists in a different vrf",
	} {
		if isGrcliExistsError([]byte(out)) {
			t.Errorf("isGrcliExistsError(%q) = true, want false", out)
		}
	}
}

// fakeGrcli swaps runGrcli for the duration of a test and records the commands
// it was asked to run.
func fakeGrcli(t *testing.T, respond func(args []string) ([]byte, error)) *[][]string {
	t.Helper()
	var calls [][]string
	original := runGrcli
	runGrcli = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return respond(args)
	}
	t.Cleanup(func() { runGrcli = original })
	return &calls
}

// grcliWithLivePort answers the two read-only queries the adopted-tap check
// makes, the way grout answers them: `interface show` lists rows without an
// MTU, and `interface show NAME` returns the one object that has it.
func grcliWithLivePort(list, detail string) func(args []string) ([]byte, error) {
	return func(args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "-j interface show "):
			return []byte(detail), nil
		case joined == "-j interface show":
			return []byte(list), nil
		default:
			return nil, nil
		}
	}
}

func batchCalls(calls [][]string, substr string) int {
	var n int
	for _, args := range calls {
		if strings.Contains(strings.Join(args, " "), substr) {
			n++
		}
	}
	return n
}

// TestApplyGrcliBatchSkipsAnAdoptedWorkloadTap covers the outage this skip
// exists for: once the CNI has moved a workload port's tap into the pod netns,
// re-issuing the port's add fails EPERM, which aborts the batch and takes the
// running pod's tap with it.
func TestApplyGrcliBatchSkipsAnAdoptedWorkloadTap(t *testing.T) {
	calls := fakeGrcli(t, grcliWithLivePort(
		`[{"name":"craeadbd8","type":"port","mode":"vrf","domain":"m2m"}]`,
		`{"name":"craeadbd8","type":"port","mode":"vrf","vrf":"m2m","mtu":1500}`))

	batch := "interface add port craeadbd8 devargs net_tap_craeadbd8,iface=craeadbd8_dp mtu 1500 vrf m2m\n" +
		"address add 10.0.0.1/32 iface craeadbd8"
	if _, err := applyGrcliBatch(context.Background(), batch); err != nil {
		t.Fatalf("applyGrcliBatch: %v", err)
	}

	if n := batchCalls(*calls, "-e interface add port craeadbd8"); n != 0 {
		t.Fatalf("applyGrcliBatch re-added the adopted tap: %v", *calls)
	}
	if n := batchCalls(*calls, "-e address add"); n != 1 {
		t.Fatalf("applyGrcliBatch skipped the rest of the batch: %v", *calls)
	}
}

// TestApplyGrcliBatchRecreatesPortsAfterARestart guards the other half: a CRA
// that comes back to an empty grout must rebuild every workload port, so the
// skip must not degrade into "never add a workload port".
func TestApplyGrcliBatchRecreatesPortsAfterARestart(t *testing.T) {
	calls := fakeGrcli(t, grcliWithLivePort(`[]`, ``))

	batch := "interface add port craeadbd8 devargs net_tap_craeadbd8,iface=craeadbd8_dp mtu 1500 vrf m2m"
	if _, err := applyGrcliBatch(context.Background(), batch); err != nil {
		t.Fatalf("applyGrcliBatch: %v", err)
	}

	if n := batchCalls(*calls, "-e interface add port craeadbd8"); n != 1 {
		t.Fatalf("applyGrcliBatch did not recreate the port: %v", *calls)
	}
}

// TestApplyGrcliBatchRejectsADriftedWorkloadPort keeps the skip honest: a live
// port that no longer matches the configuration must be reported, not quietly
// left as it is, because nothing else would ever notice.
func TestApplyGrcliBatchRejectsADriftedWorkloadPort(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detail string
	}{
		{"mtu", `{"name":"craeadbd8","mode":"vrf","vrf":"m2m","mtu":9000}`},
		{"vrf", `{"name":"craeadbd8","mode":"vrf","vrf":"c2m","mtu":1500}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeGrcli(t, grcliWithLivePort(
				`[{"name":"craeadbd8","type":"port"}]`, tc.detail))

			batch := "interface add port craeadbd8 devargs net_tap_craeadbd8,iface=craeadbd8_dp mtu 1500 vrf m2m"
			if _, err := applyGrcliBatch(context.Background(), batch); err == nil {
				t.Fatal("applyGrcliBatch accepted a drifted adopted tap")
			}
		})
	}
}

// TestApplyGrcliBatchListsInterfacesOnce: the listing is a round trip to grout
// on a path that runs on every reconcile, so it is looked up lazily and cached.
func TestApplyGrcliBatchListsInterfacesOnce(t *testing.T) {
	calls := fakeGrcli(t, grcliWithLivePort(`[]`, ``))

	batch := "interface add port craeadbd8 devargs net_tap_craeadbd8,iface=craeadbd8_dp mtu 1500 vrf m2m\n" +
		"interface add port cra99887 devargs net_tap_cra99887,iface=cra99887_dp mtu 1500 vrf m2m"
	if _, err := applyGrcliBatch(context.Background(), batch); err != nil {
		t.Fatalf("applyGrcliBatch: %v", err)
	}

	var lists int
	for _, args := range *calls {
		if strings.Join(args, " ") == "-j interface show" {
			lists++
		}
	}
	if lists != 1 {
		t.Fatalf("applyGrcliBatch listed interfaces %d times, want 1", lists)
	}
}

// TestApplyGrcliBatchIgnoresVhostPorts: a vhost-user port's backing socket
// stays grout's, so it is re-applied like any other line and must not be
// caught by the tap skip.
func TestApplyGrcliBatchIgnoresVhostPorts(t *testing.T) {
	calls := fakeGrcli(t, grcliWithLivePort(
		`[{"name":"v1a2b3c4","type":"port"}]`, `{"name":"v1a2b3c4","mtu":1500}`))

	batch := "interface add port v1a2b3c4 devargs net_vhost0,iface=/run/v1a2b3c4.sock mtu 1500 vrf m2m"
	if _, err := applyGrcliBatch(context.Background(), batch); err != nil {
		t.Fatalf("applyGrcliBatch: %v", err)
	}

	if n := batchCalls(*calls, "interface show"); n != 0 {
		t.Fatalf("applyGrcliBatch inspected grout for a vhost-user port: %v", *calls)
	}
	if n := batchCalls(*calls, "-e interface add port v1a2b3c4"); n != 1 {
		t.Fatalf("applyGrcliBatch skipped a vhost-user port: %v", *calls)
	}
}
