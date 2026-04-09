package operator

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

var _ = Describe("buildFilterItems", func() {
	It("should include IPv4 prefixes when family is IPv4", func() {
		items := []v1alpha1.VrfRouteConfigurationPrefixItem{
			{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit"},
			{CIDR: "2001:db8::/32", Seq: 20, Action: "permit"},
		}
		result, err := buildFilterItems(items, IPv4)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(HaveLen(1))
		Expect(result[0].Matcher.Prefix.Prefix).To(Equal("10.0.0.0/8"))
		Expect(result[0].Action.Type).To(Equal(v1alpha1.Accept))
	})

	It("should include IPv6 prefixes when family is IPv6", func() {
		items := []v1alpha1.VrfRouteConfigurationPrefixItem{
			{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit"},
			{CIDR: "2001:db8::/32", Seq: 20, Action: "permit"},
		}
		result, err := buildFilterItems(items, IPv6)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(HaveLen(1))
		Expect(result[0].Matcher.Prefix.Prefix).To(Equal("2001:db8::/32"))
		Expect(result[0].Action.Type).To(Equal(v1alpha1.Accept))
	})

	It("should include both IPv4 and IPv6 prefixes when family is Both", func() {
		items := []v1alpha1.VrfRouteConfigurationPrefixItem{
			{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit"},
			{CIDR: "2001:db8::/32", Seq: 20, Action: "permit"},
		}
		result, err := buildFilterItems(items, Both)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(HaveLen(2))
	})

	It("should map 'permit' action to Accept", func() {
		items := []v1alpha1.VrfRouteConfigurationPrefixItem{
			{CIDR: "192.168.0.0/16", Seq: 10, Action: "permit"},
		}
		result, err := buildFilterItems(items, IPv4)
		Expect(err).ToNot(HaveOccurred())
		Expect(result[0].Action.Type).To(Equal(v1alpha1.Accept))
	})

	It("should map non-permit action to Reject", func() {
		items := []v1alpha1.VrfRouteConfigurationPrefixItem{
			{CIDR: "192.168.0.0/16", Seq: 10, Action: "deny"},
		}
		result, err := buildFilterItems(items, IPv4)
		Expect(err).ToNot(HaveOccurred())
		Expect(result[0].Action.Type).To(Equal(v1alpha1.Reject))
	})

	It("should return error on invalid CIDR", func() {
		items := []v1alpha1.VrfRouteConfigurationPrefixItem{
			{CIDR: "not-a-cidr", Seq: 10, Action: "permit"},
		}
		_, err := buildFilterItems(items, IPv4)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to parse CIDR"))
	})

	It("should sort items by Seq before processing", func() {
		items := []v1alpha1.VrfRouteConfigurationPrefixItem{
			{CIDR: "192.168.2.0/24", Seq: 30, Action: "permit"},
			{CIDR: "192.168.1.0/24", Seq: 10, Action: "deny"},
			{CIDR: "192.168.3.0/24", Seq: 20, Action: "permit"},
		}
		result, err := buildFilterItems(items, IPv4)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(HaveLen(3))
		// After sort by Seq: 10, 20, 30
		Expect(result[0].Matcher.Prefix.Prefix).To(Equal("192.168.1.0/24"))
		Expect(result[1].Matcher.Prefix.Prefix).To(Equal("192.168.3.0/24"))
		Expect(result[2].Matcher.Prefix.Prefix).To(Equal("192.168.2.0/24"))
	})

	It("should set GE and LE on prefix matcher", func() {
		ge := 24
		le := 32
		items := []v1alpha1.VrfRouteConfigurationPrefixItem{
			{CIDR: "10.0.0.0/8", Seq: 10, Action: "permit", GE: &ge, LE: &le},
		}
		result, err := buildFilterItems(items, IPv4)
		Expect(err).ToNot(HaveOccurred())
		Expect(result[0].Matcher.Prefix.Ge).To(Equal(&ge))
		Expect(result[0].Matcher.Prefix.Le).To(Equal(&le))
	})

	DescribeTable("family filtering",
		func(cidr string, family AddressFamily, expectIncluded bool) {
			items := []v1alpha1.VrfRouteConfigurationPrefixItem{
				{CIDR: cidr, Seq: 1, Action: "permit"},
			}
			result, err := buildFilterItems(items, family)
			Expect(err).ToNot(HaveOccurred())
			if expectIncluded {
				Expect(result).To(HaveLen(1))
			} else {
				Expect(result).To(BeEmpty())
			}
		},
		Entry("IPv4 CIDR with IPv4 family", "10.0.0.0/8", IPv4, true),
		Entry("IPv4 CIDR with IPv6 family", "10.0.0.0/8", IPv6, false),
		Entry("IPv6 CIDR with IPv4 family", "2001:db8::/32", IPv4, false),
		Entry("IPv6 CIDR with IPv6 family", "2001:db8::/32", IPv6, true),
		Entry("IPv4 CIDR with Both family", "10.0.0.0/8", Both, true),
		Entry("IPv6 CIDR with Both family", "2001:db8::/32", Both, true),
	)
})
