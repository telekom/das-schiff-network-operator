package framework

import (
	"context"
	"fmt"
	"time"
)

// Poll calls condition repeatedly at interval until it returns true, an error,
// or the context expires.
func Poll(ctx context.Context, interval time.Duration, condition func() (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Check immediately.
	done, err := condition()
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for condition: %w", ctx.Err())
		case <-ticker.C:
			done, err := condition()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}
}

// WaitForCondition is a convenience wrapper around Poll with a timeout.
func WaitForCondition(timeout, interval time.Duration, condition func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return Poll(ctx, interval, condition)
}

// Eventually retries f until it succeeds (returns nil) or the timeout expires.
func Eventually(timeout, interval time.Duration, f func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return Poll(ctx, interval, func() (bool, error) {
		if err := f(); err != nil {
			return false, nil // Retry
		}
		return true, nil
	})
}
