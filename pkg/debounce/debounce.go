package debounce

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/util/workqueue"
)

// Debouncer struct.
type Debouncer struct {
	once     sync.Once
	queue    workqueue.TypedDelayingInterface[string]
	done     chan struct{}
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
		done:            make(chan struct{}),
	}
}

func (d *Debouncer) debounceRoutine(ctx context.Context) {
	defer close(d.done)
	for {
		key, shutdown := d.queue.Get()
		if shutdown {
			return
		}
		if ctx.Err() == nil {
			if err := d.function(ctx); err != nil && ctx.Err() == nil {
				d.logger.Error(err, "error debouncing")
				d.queue.AddAfter(key, d.debounceTime)
			}
		}
		d.queue.Done(key)
	}
}

func (d *Debouncer) initializeWorker() {
	d.queue = workqueue.NewTypedDelayingQueue[string]()
	go d.debounceRoutine(d.internalCtxFunc()) //nolint:contextcheck // worker uses the internal lifecycle context
}

// Debounce schedules work after debounceTime, coalescing pending triggers.
// Triggers during execution schedule a serialized follow-up when their delay expires.
// The incoming ctx is used only to signal that the caller wants to debounce; the actual
// goroutine always runs with the Debouncer's internal context so it is not canceled when
// a short-lived reconcile context expires.
func (d *Debouncer) Debounce(_ context.Context) {
	d.once.Do(d.initializeWorker)
	d.queue.AddAfter("reconcile", d.debounceTime)
}

// Start binds worker shutdown to the manager's lifetime.
func (d *Debouncer) Start(ctx context.Context) error {
	<-ctx.Done()
	d.Stop()
	return nil
}

// NeedLeaderElection keeps shutdown registered even on a non-leader manager.
// Only leader-elected controllers and startup hooks trigger work.
func (*Debouncer) NeedLeaderElection() bool {
	return false
}

// Stop cancels in-flight work, discards pending work and waits for the worker.
// The callback must honor cancellation; Stop must not be called from the callback.
func (d *Debouncer) Stop() {
	d.cancel()
	d.once.Do(d.initializeWorker)
	d.queue.ShutDown()
	<-d.done
}
