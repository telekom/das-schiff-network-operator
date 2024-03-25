package managerconfig

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestManagerConfig(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t,
		"ManagerConfig Suite")
}

var _ = Describe("Load()", func() {
	It("returns error if cannot open file", func() {
		_, err := Load("", runtime.NewScheme())
		Expect(err).To(HaveOccurred())
	})
	It("returns error if config is invalid", func() {
		_, err := Load("testdata/invalid_config.yaml", runtime.NewScheme())
		Expect(err).To(HaveOccurred())
	})
	It("returns no error", func() {
		_, err := Load("testdata/config.yaml", runtime.NewScheme())
		Expect(err).ToNot(HaveOccurred())
	})
})
