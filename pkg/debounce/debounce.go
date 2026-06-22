package debounce

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
)

// Debouncer struct.
type Debouncer struct {
	// Used for atomic operations
	scheduled             atomic.Bool
	calledDuringExecution atomic.Bool
	// Stores the function as an interface so we can use reflect
	function func(context.Context) error
	// Duration between function call
	debounceTime time.Duration
	logger       logr.Logger
	// cancel cancels the internal long-lived context used by debounced goroutines.
	cancel context.CancelFunc
	// internalCtxFunc returns the Debouncer's internal context. This avoids
	// storing a context.Context in the struct (containedctx linter).
	internalCtxFunc func() context.Context
}

// Create a new debouncer.
func NewDebouncer(function func(context.Context) error, debounceTime time.Duration, logger logr.Logger) *Debouncer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Debouncer{
		function:        function,
		debounceTime:    debounceTime,
		logger:          logger,
		cancel:          cancel,
		internalCtxFunc: func() context.Context { return ctx },
	}
}

func (d *Debouncer) debounceRoutine(ctx context.Context) {
	for {
		// First sleep for the debounceTime
		time.Sleep(d.debounceTime)
		d.calledDuringExecution.Store(false)
		err := d.function(ctx)
		if err == nil {
			// If debounce was called during execution run debounceRoutine again otherwise reset
			// scheduled to false
			if d.calledDuringExecution.CompareAndSwap(true, false) {
				d.debounceRoutine(ctx)
			} else {
				d.scheduled.Store(false)
			}
			break
		}
		d.logger.Error(err, "error debouncing")
	}
}

// Run function. First run will be in debounceTime, runs will be separated by debounceTime.
// The incoming ctx is used only to signal that the caller wants to debounce; the actual
// goroutine always runs with the Debouncer's internal context so it is not canceled when
// a short-lived reconcile context expires.
func (d *Debouncer) Debounce(_ context.Context) {
	// If we haven't scheduled a goroutine yet, set scheduled=false and run goroutine
	// We use atomic compare-and-swap to first check if scheduled equals false (not yet scheduled)
	// and then swap the value with true
	// Always set calledDuringExection to true but reset it to false if we schedule it the first time.
	// This way a debounce during running execution (scheduled is still true, calledDuringExecution will
	// be true) will run the debounced routine once again
	d.calledDuringExecution.Store(true)
	if d.scheduled.CompareAndSwap(false, true) {
		go d.debounceRoutine(d.internalCtxFunc()) //nolint:contextcheck // context is from internal closure, not request-scoped
	}
}

// Stop cancels the Debouncer's internal context, which will cause any in-progress
// debounced goroutine to observe a canceled context on its next function call.
func (d *Debouncer) Stop() {
	d.cancel()
}
