package debounce

import (
	"context"
	"sync/atomic"
	"time"
)

// Debouncer struct
type Debouncer struct {
	// Used for atomic operations
	scheduled int32
	// Stores the function as an interface so we can use reflect
	function func(context.Context) error
	// Duration between function call
	debounceTime time.Duration
}

// Create a new debouncer
func NewDebouncer(function func(context.Context) error, debounceTime time.Duration) *Debouncer {
	return &Debouncer{
		scheduled:    0,
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
			// Reset scheduled to 0
			atomic.StoreInt32(&d.scheduled, 0)
			break
		}
	}
}

// Run function. First run will be in debounceTime, runs will be seperated by debounceTime
func (d *Debouncer) Debounce(ctx context.Context) {
	// If we haven't scheduled a goroutine yet, set scheduled=0 and run goroutine
	// We use atomic compare-and-swap to first check if scheduled equals 0 (not yet scheduled)
	// and then swap the value with 1
	if atomic.CompareAndSwapInt32(&d.scheduled, 0, 1) {
		go d.debounceRoutine(ctx)
	}
}
