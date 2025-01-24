package cra

import "github.com/telekom/das-schiff-network-operator/pkg/nl"

type CRAConfiguration struct {
	NetlinkConfiguration nl.NetlinkConfiguration `json:"netlink"`
	FRRConfiguration     string                  `json:"frr"`
}
