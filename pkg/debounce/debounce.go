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
	scheduled             *atomic.Int32
	calledDuringExecution *atomic.Int32
	// Stores the function as an interface so we can use reflect
	function func(context.Context) error
	// Duration between function call
	debounceTime time.Duration
}

// Create a new debouncer
func NewDebouncer(function func(context.Context) error, debounceTime time.Duration) *Debouncer {
	return &Debouncer{
		scheduled:             &atomic.Int32{},
		calledDuringExecution: &atomic.Int32{},
		function:              function,
		debounceTime:          debounceTime,
	}
}

func (d *Debouncer) debounceRoutine(ctx context.Context) {
	for {
		// First sleep for the debounceTime
		time.Sleep(d.debounceTime)

		err := d.function(ctx)
		if err == nil {
			if d.calledDuringExecution.CompareAndSwap(1, 0) {
				d.debounceRoutine(ctx)
			} else {
				// Reset scheduled to 0
				d.scheduled.Store(0)
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
	d.calledDuringExecution.Store(1)
	if d.scheduled.CompareAndSwap(0, 1) {
		d.calledDuringExecution.Store(0)
		go d.debounceRoutine(ctx)
	}
}
