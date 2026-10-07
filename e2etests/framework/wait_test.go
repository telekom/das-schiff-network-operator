// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPollConditionContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sentinel := errors.New("condition failure")
	for _, tc := range []struct {
		name string
		done bool
		err  error
	}{
		{name: "success even with cancelled context", done: true},
		{name: "error takes precedence over success", done: true, err: sentinel},
		{name: "condition cancellation returned unchanged", err: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := Poll(ctx, time.Hour, func() (bool, error) {
				calls++
				return tc.done, tc.err
			})
			if err != tc.err || calls != 1 {
				t.Fatalf("got error %v after %d calls; want %v after one immediate call", err, calls, tc.err)
			}
		})
	}
}

func TestPollCancelsBetweenAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Poll(ctx, time.Hour, func() (bool, error) {
		cancel()
		return false, nil
	})
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "timed out waiting for condition: ") {
		t.Fatalf("cancellation lost diagnostic or cause: %v", err)
	}
}
