package configmap

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/pkg/nodeconfig"
)

func TestConfigMap(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t,
		"ConfigMap Suite")
}

var _ = Describe("ConfigMap", func() {
	Context("Get() should", func() {
		It("return no error and nil value if element does not exist in the map", func() {
			m := ConfigMap{}
			cfg, err := m.Get("nodeName")
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg).To(BeNil())
		})
		It("return error if cannot type assert map element to nodeconfig.Config pointer", func() {
			name := "nodeName"
			m := ConfigMap{}
			m.Store(name, "someInvalidValue")
			cfg, err := m.Get(name)
			Expect(err).To(HaveOccurred())
			Expect(cfg).To(BeNil())
		})
		It("return no error if can get the value from the map", func() {
			name := "nodeName"
			m := ConfigMap{}
			testCfg := &nodeconfig.Config{}
			m.Store(name, testCfg)
			cfg, err := m.Get(name)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg).To(Equal(testCfg))
		})
	})
	Context("GetSlice() should", func() {
		It("return empty slice if there are no values in the map", func() {
			m := ConfigMap{}
			slice, err := m.GetSlice()
			Expect(err).ToNot(HaveOccurred())
			Expect(slice).To(BeEmpty())
		})
		It("return error if key is not of type string", func() {
			m := ConfigMap{}
			m.Store(0, &nodeconfig.Config{})
			slice, err := m.GetSlice()
			Expect(err).To(HaveOccurred())
			Expect(slice).To(BeNil())
		})
		It("be able to contain nil value", func() {
			m := ConfigMap{}
			m.Store("nodeName", nil)
			slice, err := m.GetSlice()
			Expect(err).ToNot(HaveOccurred())
			Expect(slice).To(HaveLen(1))
		})
		It("return error if cannot type assert map element to nodeconfig.Config pointer", func() {
			m := ConfigMap{}
			m.Store("nodeName", "someInvalidValue")
			slice, err := m.GetSlice()
			Expect(err).To(HaveOccurred())
			Expect(slice).To(BeNil())
		})
		It("return no error", func() {
			m := ConfigMap{}
			m.Store("nodeName", &nodeconfig.Config{})
			m.Store("nodeName2", &nodeconfig.Config{})
			slice, err := m.GetSlice()
			Expect(err).ToNot(HaveOccurred())
			Expect(slice).To(HaveLen(2))
		})
	})
})
