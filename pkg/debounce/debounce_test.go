package debounce

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	logger logr.Logger
)

var _ = BeforeSuite(func() {

})

func TestDebounce(t *testing.T) {
	RegisterFailHandler(Fail)
	logger = ctrl.Log.WithName("debounce-test")
	RunSpecs(t,
		"Debounce Suite")
}

var _ = Describe("debounce", func() {
	Context("NewDebouncer() should", func() {
		It("create new debouncer", func() {
			d := NewDebouncer(nil, time.Millisecond, logger)
			Expect(d).ToNot(BeNil())
		})
	})
})

// TestDebounce_CanceledRequestCtxDoesNotCancelDebouncedRun verifies that canceling the
// request-scoped context passed to Debounce() does NOT cancel the debounced goroutine.
// The debouncer must use its own internal long-lived context for async work.
func TestDebounce_CanceledRequestCtxDoesNotCancelDebouncedRun(t *testing.T) {
	var (
		mu        sync.Mutex
		ctxPassed context.Context
	)

	fn := func(ctx context.Context) error {
		mu.Lock()
		ctxPassed = ctx
		mu.Unlock()
		return nil
	}

	d := NewDebouncer(fn, 10*time.Millisecond, logr.Discard())
	defer d.Stop()

	// Create a request-scoped context and immediately cancel it.
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Debounce with the already-canceled request context.
	d.Debounce(reqCtx)

	// Wait enough for the debounce routine to fire.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	got := ctxPassed
	mu.Unlock()

	if got == nil {
		t.Fatal("debounced function was not called; expected it to run regardless of canceled request ctx")
	}
	if err := got.Err(); err != nil {
		t.Errorf("debounced function received canceled context (err=%v); expected internal non-canceled context", err)
	}
}

// TestDebounce_StopCancelsInternalContext verifies that calling Stop() cancels the
// Debouncer's internal context, signaling any long-running debounced work.
func TestDebounce_StopCancelsInternalContext(t *testing.T) {
	d := NewDebouncer(func(_ context.Context) error { return nil }, time.Hour, logr.Discard())

	if err := d.internalCtxFunc().Err(); err != nil {
		t.Fatalf("internal context should be live before Stop(); got: %v", err)
	}

	d.Stop()

	if err := d.internalCtxFunc().Err(); err == nil {
		t.Fatal("internal context should be canceled after Stop()")
	}
}
