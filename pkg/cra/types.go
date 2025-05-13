package cra

import "github.com/telekom/das-schiff-network-operator/pkg/nl"

type Configuration struct {
	NetlinkConfiguration nl.NetlinkConfiguration `json:"netlink"`
	FRRConfiguration     string                  `json:"frr"`
}
