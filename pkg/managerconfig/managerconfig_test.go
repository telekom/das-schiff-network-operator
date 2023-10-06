package managerconfig

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
)

var _ = BeforeSuite(func() {

})

func TestHealthCheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t,
		"HealthCheck Suite")
}

var _ = Describe("managerconfig", func() {
	Context("Load() should", func() {
		It("return error if cannot open file", func() {
			_, err := Load("", runtime.NewScheme())
			Expect(err).To(HaveOccurred())
		})
		It("return error if config is invalid", func() {
			_, err := Load("testdata/invalid_config.yaml", runtime.NewScheme())
			Expect(err).To(HaveOccurred())
		})
		It("return no error", func() {
			_, err := Load("testdata/config.yaml", runtime.NewScheme())
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
