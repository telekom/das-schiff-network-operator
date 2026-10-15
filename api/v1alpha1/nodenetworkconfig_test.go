package v1alpha1

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NodeNetworkConfig success history", func() {
	It("persists successful provenance independently of recovery status", func() {
		config := NewEmptyConfig("success-history-node")
		config.Spec.Revision, config.Spec.ConfigHash = "revision-one", "config-one"
		Expect(k8sClient.Create(ctx, config)).To(Succeed())
		generation := config.Generation
		config.Status.LastAppliedRevision = config.Spec.Revision
		config.Status.LastAppliedConfigHash = config.Spec.ConfigHash
		config.Status.LastSuccessfulRevision = config.Spec.Revision
		config.Status.LastSuccessfulConfigHash = config.Spec.ConfigHash

		for _, status := range []string{"provisioned", "provisioning", "invalid"} {
			config.Status.ConfigStatus, config.Status.LastUpdate = status, metav1.Now()
			Expect(k8sClient.Status().Update(ctx, config)).To(Succeed())
			stored := &NodeNetworkConfig{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), stored)).To(Succeed())
			Expect(stored.Status.LastSuccessfulRevision).To(Equal(config.Spec.Revision))
			Expect(stored.Status.LastSuccessfulConfigHash).To(Equal(config.Spec.ConfigHash))
			Expect(stored.Status.ConfigStatus).To(Equal(status))
			Expect(stored.Generation).To(Equal(generation), "status writes must not trigger spec-change watches")
			Expect(stored.HasSuccessfulConfig()).To(BeTrue())
			config = stored
		}

		config.Spec.ConfigHash = "unproven-config"
		Expect(k8sClient.Update(ctx, config)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), config)).To(Succeed())
		Expect(config.Generation).To(BeNumerically(">", generation), "desired config changes must trigger spec-change watches")
		Expect(config.HasSuccessfulConfig()).To(BeFalse())
		Expect(k8sClient.Delete(ctx, config)).To(Succeed())
	})
})
