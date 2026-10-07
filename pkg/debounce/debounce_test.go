// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
// SPDX-License-Identifier: Apache-2.0
// Debounce worker lifecycle regression coverage.

package debounce

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not make progress")
	}
}

func TestCanceledRequestDoesNotCancelWork(t *testing.T) {
	called := make(chan struct{})
	var callbackErr error
	d := NewDebouncer(func(ctx context.Context) error {
		callbackErr = ctx.Err()
		close(called)
		return nil
	}, time.Millisecond, logr.Discard())
	defer d.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.Debounce(ctx)
	await(t, called)
	if callbackErr != nil {
		t.Fatalf("request cancellation reached the worker: %v", callbackErr)
	}
}

func TestStopDiscardsPendingWork(t *testing.T) {
	var calls atomic.Int32
	d := NewDebouncer(func(context.Context) error {
		calls.Add(1)
		return nil
	}, time.Hour, logr.Discard())
	d.Debounce(context.Background())
	d.Stop()
	d.Debounce(context.Background())
	d.Stop()
	if calls.Load() != 0 {
		t.Fatal("Stop allowed a pending or post-shutdown callback")
	}
}

func TestStopCancelsAndJoinsInFlightRetry(t *testing.T) {
	entered, canceled, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	d := NewDebouncer(func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("retry must stop on cancellation")
	}, time.Millisecond, logr.Discard())
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		d.Stop()
	})
	d.Debounce(context.Background())
	await(t, entered)
	go func() {
		d.Stop()
		close(stopped)
	}()
	await(t, canceled)
	select {
	case <-stopped:
		t.Fatal("Stop returned before in-flight cleanup completed")
	default:
	}
	close(release)
	await(t, stopped)
}

func TestTriggerDuringExecutionSchedulesSerialFollowUp(t *testing.T) {
	entered, release, followUp := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	d := NewDebouncer(func(ctx context.Context) error {
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
			t.Error("unexpected duplicate callback")
		}
		return nil
	}, time.Millisecond, logr.Discard())
	defer d.Stop()
	d.Debounce(context.Background())
	await(t, entered)
	d.Debounce(context.Background())
	select {
	case <-followUp:
		t.Fatal("callbacks ran concurrently")
	default:
	}
	close(release)
	await(t, followUp)
}

func TestErrorsRetryWithoutAnotherTrigger(t *testing.T) {
	retried := make(chan struct{})
	var calls atomic.Int32
	d := NewDebouncer(func(context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("transient failure")
		}
		close(retried)
		return nil
	}, time.Millisecond, logr.Discard())
	defer d.Stop()
	d.Debounce(context.Background())
	await(t, retried)
}

func TestManagerLifetimeStopsWorker(t *testing.T) {
	d := NewDebouncer(func(context.Context) error { return nil }, time.Hour, logr.Discard())
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		if err := d.Start(ctx); err != nil {
			t.Errorf("manager runnable failed: %v", err)
		}
		close(finished)
	}()
	d.Debounce(context.Background())
	cancel()
	await(t, finished)
	d.Stop()
}
