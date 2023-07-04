package debounce

import (
	"context"
	"sync/atomic"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Debouncer struct
type Debouncer struct {
	// Used for atomic operations
	scheduled             atomic.Bool
	calledDuringExecution atomic.Bool
	// Stores the function as an interface so we can use reflect
	function func(context.Context) error
	// Duration between function call
	debounceTime time.Duration
}

// Create a new debouncer
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

		err := d.function(ctx)
		if err == nil {
			if d.calledDuringExecution.CompareAndSwap(true, false) {
				d.debounceRoutine(ctx)
			} else {
				// Reset scheduled to 0
				d.scheduled.Store(false)
			}
			break
		} else {
			log.FromContext(ctx).Error(err, "error debouncing")
		}
	}
}

// Run function. First run will be in debounceTime, runs will be seperated by debounceTime
func (d *Debouncer) Debounce(ctx context.Context) {
	// If we haven't scheduled a goroutine yet, set scheduled=0 and run goroutine
	// We use atomic compare-and-swap to first check if scheduled equals 0 (not yet scheduled)
	// and then swap the value with 1
	d.calledDuringExecution.Store(true)
	if d.scheduled.CompareAndSwap(false, true) {
		d.calledDuringExecution.Store(false)
		go d.debounceRoutine(ctx)
	}
}
