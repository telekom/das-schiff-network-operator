// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
// SPDX-License-Identifier: Apache-2.0

package setup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitForRetriesConditionErrors(t *testing.T) {
	calls := 0
	err := WaitFor("setup ready", time.Second, time.Millisecond, func() (bool, error) {
		calls++
		return calls == 2, errors.New("transient diagnostic")
	})
	if err != nil || calls != 2 {
		t.Fatalf("got %v after %d attempts; want success after retrying the diagnostic", err, calls)
	}
}

func TestWaitForImmediateSuccess(t *testing.T) {
	calls := 0
	err := WaitFor("setup ready", 0, time.Hour, func() (bool, error) {
		calls++
		return true, nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("got %v after %d attempts; want immediate success", err, calls)
	}
}

func TestWaitForDeadlineInterruptsInterval(t *testing.T) {
	start := time.Now()
	err := WaitFor("setup ready", time.Millisecond, time.Hour, func() (bool, error) {
		return false, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "timeout waiting for setup ready (1ms): ") {
		t.Fatalf("timeout lost description or cause: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("deadline did not interrupt the interval: %v", elapsed)
	}
}
