package cra

import "github.com/telekom/das-schiff-network-operator/pkg/nl"

// PolicyRoute defines a source-based routing rule to be installed via netlink.
type PolicyRoute struct {
	SrcPrefix *string `json:"srcPrefix,omitempty"`
	DstPrefix *string `json:"dstPrefix,omitempty"`
	SrcPort   *uint16 `json:"srcPort,omitempty"`
	DstPort   *uint16 `json:"dstPort,omitempty"`
	Protocol  *string `json:"protocol,omitempty"`
	Vrf       string  `json:"vrf,omitempty"`
}

type Configuration struct {
	NetlinkConfiguration nl.NetlinkConfiguration `json:"netlink"`
	FRRConfiguration     string                  `json:"frr"`
	PolicyRoutes         []PolicyRoute           `json:"policyRoutes,omitempty"`
}
