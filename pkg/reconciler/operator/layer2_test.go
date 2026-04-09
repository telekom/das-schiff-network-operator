package operator

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

var _ = Describe("Layer2 building", func() {
	Describe("checkL2Duplicates", func() {
		It("should return nil when no duplicates exist", func() {
			configs := []v1alpha1.Layer2NetworkConfiguration{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "l2-a"},
					Spec:       v1alpha1.Layer2NetworkConfigurationSpec{VNI: 1000},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "l2-b"},
					Spec:       v1alpha1.Layer2NetworkConfigurationSpec{VNI: 2000},
				},
			}
			Expect(checkL2Duplicates(configs)).To(Succeed())
		})

		It("should return error when duplicate VNI found", func() {
			configs := []v1alpha1.Layer2NetworkConfiguration{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "l2-a"},
					Spec:       v1alpha1.Layer2NetworkConfigurationSpec{VNI: 1000},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "l2-b"},
					Spec:       v1alpha1.Layer2NetworkConfigurationSpec{VNI: 1000},
				},
			}
			err := checkL2Duplicates(configs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("VNI"))
			Expect(err.Error()).To(ContainSubstring("l2-a"))
			Expect(err.Error()).To(ContainSubstring("l2-b"))
		})

		It("should return nil for empty slice", func() {
			Expect(checkL2Duplicates(nil)).To(Succeed())
		})

		It("should return nil for single item", func() {
			configs := []v1alpha1.Layer2NetworkConfiguration{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "l2-a"},
					Spec:       v1alpha1.Layer2NetworkConfigurationSpec{VNI: 1000},
				},
			}
			Expect(checkL2Duplicates(configs)).To(Succeed())
		})
	})

	Describe("buildNodeLayer2", func() {
		It("should skip layer2 entries that don't match node selector", func() {
			node := makeNode("node1", true)
			node.Labels = map[string]string{"role": "worker"}

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-100",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:  100,
								VNI: 1000,
								MTU: 1500,
								NodeSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"role": "master"},
								},
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := buildNodeLayer2(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.Layer2s).To(BeEmpty())
		})

		It("should create basic layer2 for matching nodes", func() {
			node := makeNode("node1", true)

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-100",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:  100,
								VNI: 1000,
								MTU: 1500,
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := buildNodeLayer2(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.Layer2s).To(HaveKey("100"))
			l2 := c.Spec.Layer2s["100"]
			Expect(l2.VNI).To(Equal(uint32(1000)))
			Expect(l2.VLAN).To(Equal(uint16(100)))
			Expect(l2.MTU).To(Equal(uint16(1500)))
		})

		It("should create IRB when anycast gateways are set", func() {
			node := makeNode("node1", true)

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-100",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:              100,
								VNI:             1000,
								MTU:             1500,
								VRF:             "tenant-a",
								AnycastGateways: []string{"10.0.0.1/24"},
								AnycastMac:      "aa:bb:cc:dd:ee:ff",
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := buildNodeLayer2(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.Layer2s["100"].IRB).ToNot(BeNil())
			Expect(c.Spec.Layer2s["100"].IRB.VRF).To(Equal("tenant-a"))
			Expect(c.Spec.Layer2s["100"].IRB.IPAddresses).To(ContainElement("10.0.0.1/24"))
			Expect(c.Spec.Layer2s["100"].IRB.MACAddress).To(Equal("aa:bb:cc:dd:ee:ff"))
		})

		It("should return error for duplicate layer2 ID", func() {
			node := makeNode("node1", true)

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-100a",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:  100,
								VNI: 1000,
								MTU: 1500,
							},
						},
						{
							Name: "l2-100b",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:  100,
								VNI: 2000,
								MTU: 1500,
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := buildNodeLayer2(node, revision, c)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate Layer2 ID"))
		})

		It("should sort layer2 by ID before processing", func() {
			node := makeNode("node1", true)

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-200",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:  200,
								VNI: 2000,
								MTU: 1500,
							},
						},
						{
							Name: "l2-100",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:  100,
								VNI: 1000,
								MTU: 1500,
							},
						},
					},
				},
			}

			c := &v1alpha1.NodeNetworkConfig{}
			err := buildNodeLayer2(node, revision, c)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Spec.Layer2s).To(HaveLen(2))
			Expect(c.Spec.Layer2s).To(HaveKey("100"))
			Expect(c.Spec.Layer2s).To(HaveKey("200"))
		})
	})

	Describe("buildNetplanVLANs", func() {
		It("should create VLAN device for matching nodes", func() {
			node := makeNode("node1", true)

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-100",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:  100,
								VNI: 1000,
								MTU: 1500,
							},
						},
					},
				},
			}

			vlans, err := buildNetplanVLANs(node, revision)
			Expect(err).ToNot(HaveOccurred())
			Expect(vlans).To(HaveKey("vlan.100"))
		})

		It("should skip layer2 entries that don't match node selector", func() {
			node := makeNode("node1", true)
			node.Labels = map[string]string{"role": "worker"}

			revision := &v1alpha1.NetworkConfigRevision{
				Spec: v1alpha1.NetworkConfigRevisionSpec{
					Layer2: []v1alpha1.Layer2Revision{
						{
							Name: "l2-100",
							Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{
								ID:  100,
								VNI: 1000,
								MTU: 1500,
								NodeSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"role": "master"},
								},
							},
						},
					},
				},
			}

			vlans, err := buildNetplanVLANs(node, revision)
			Expect(err).ToNot(HaveOccurred())
			Expect(vlans).To(BeEmpty())
		})

		It("should return empty map for empty revision", func() {
			node := makeNode("node1", true)
			revision := &v1alpha1.NetworkConfigRevision{}

			vlans, err := buildNetplanVLANs(node, revision)
			Expect(err).ToNot(HaveOccurred())
			Expect(vlans).To(BeEmpty())
		})
	})
})
