package framework

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// Poll calls condition repeatedly at interval until it returns true, an error,
// or the context expires.
// It always checks immediately, even if ctx is already canceled, and returns
// condition errors unchanged. Cancellation between attempts wraps ctx.Err().
func Poll(ctx context.Context, interval time.Duration, condition func() (bool, error)) error {
	var conditionErr error
	err := wait.PollUntilContextCancel(ctx, interval, true, func(context.Context) (bool, error) {
		var done bool
		done, conditionErr = condition()
		return done, conditionErr
	})
	if conditionErr != nil {
		return conditionErr
	}
	if err != nil {
		return fmt.Errorf("timed out waiting for condition: %w", err)
	}
	return nil
}
