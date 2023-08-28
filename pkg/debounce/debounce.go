package debounce

import (
	"context"
	"sync/atomic"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
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
}

// Create a new debouncer.
func NewDebouncer(function func(context.Context) error, debounceTime time.Duration) *Debouncer {
	return &Debouncer{
		function:     function,
		debounceTime: debounceTime,
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
		log.FromContext(ctx).Error(err, "error debouncing")
	}
}

// Run function. First run will be in debounceTime, runs will be separated by debounceTime.
func (d *Debouncer) Debounce(ctx context.Context) {
	// If we haven't scheduled a goroutine yet, set scheduled=false and run goroutine
	// We use atomic compare-and-swap to first check if scheduled equals false (not yet scheduled)
	// and then swap the value with true
	// Always set calledDuringExection to true but reset it to false if we schedule it the first time.
	// This way a debounce during running execution (scheduled is still true, calledDuringExecution will
	// be true) will run the debounced routine once again
	d.calledDuringExecution.Store(true)
	if d.scheduled.CompareAndSwap(false, true) {
		go d.debounceRoutine(ctx)
	}
}
