package config

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const operatorConfigEnv = "OPERATOR_CONFIG"

func TestDebounce(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t,
		"Config Suite")
}

var _ = Describe("LoadConfig()", func() {
	It("returns error if cannot read config", func() {
		oldEnv, isSet := os.LookupEnv(operatorConfigEnv)
		os.Setenv(operatorConfigEnv, "some-invalid-path")
		_, err := LoadConfig()
		Expect(err).To(HaveOccurred())
		if isSet {
			err = os.Setenv(operatorConfigEnv, oldEnv)
			Expect(err).ToNot(HaveOccurred())
		} else {
			err = os.Unsetenv(operatorConfigEnv)
			Expect(err).ToNot(HaveOccurred())
		}
	})
	It("returns error if cannot unmarshal config", func() {
		oldEnv, isSet := os.LookupEnv(operatorConfigEnv)
		os.Setenv(operatorConfigEnv, "./testdata/invalidConfig.yaml")
		_, err := LoadConfig()
		Expect(err).To(HaveOccurred())
		if isSet {
			err = os.Setenv(operatorConfigEnv, oldEnv)
			Expect(err).ToNot(HaveOccurred())
		} else {
			err = os.Unsetenv(operatorConfigEnv)
			Expect(err).ToNot(HaveOccurred())
		}
	})
	It("returns no error", func() {
		oldEnv, isSet := os.LookupEnv(operatorConfigEnv)
		os.Setenv(operatorConfigEnv, "./testdata/config.yaml")
		_, err := LoadConfig()
		Expect(err).ToNot(HaveOccurred())
		if isSet {
			err = os.Setenv(operatorConfigEnv, oldEnv)
			Expect(err).ToNot(HaveOccurred())
		} else {
			err = os.Unsetenv(operatorConfigEnv)
			Expect(err).ToNot(HaveOccurred())
		}
	})
})
