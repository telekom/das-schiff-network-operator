package debounce

import (
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
