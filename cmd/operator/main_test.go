// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
// SPDX-License-Identifier: Apache-2.0
// Leader-election worker routing regression coverage.

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
)

func TestLeaderElectionQueuesStartupBehindActiveReconciliation(t *testing.T) {
	entered, release, followUp := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	cr := &operator.ConfigReconciler{
		Debouncer: debounce.NewDebouncer(func(ctx context.Context) error {
			switch calls.Add(1) {
			case 1:
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			case 2:
				close(followUp)
			default:
				t.Error("unexpected duplicate reconciliation")
			}
			return nil
		}, time.Millisecond, logr.Discard()),
	}
	defer cr.Stop()
	cr.Reconcile(context.Background())
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("watch event did not start reconciliation")
	}

	if err := newOnLeaderElectionEvent(cr).Start(context.Background()); err != nil {
		t.Fatalf("startup failed to enqueue reconciliation: %v", err)
	}
	select {
	case <-followUp:
		t.Fatal("startup reconciliation overlapped the active worker")
	default:
	}
	close(release)
	select {
	case <-followUp:
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not run through the shared worker")
	}
}
