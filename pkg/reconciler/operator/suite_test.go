package operator

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

var testScheme *runtime.Scheme

func TestOperator(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Operator Reconciler Suite")
}

var _ = BeforeSuite(func() {
	testScheme = runtime.NewScheme()
	Expect(v1alpha1.AddToScheme(testScheme)).To(Succeed())
	Expect(corev1.AddToScheme(testScheme)).To(Succeed())
})
