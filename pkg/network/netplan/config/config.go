package config

import (
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
)

type Config interface {
	Reset() netplan.Error
	Get() (*netplan.State, netplan.Error)
	Set(state *netplan.State) netplan.Error
	Apply() netplan.Error
	IsSynced() bool
	Discard() netplan.Error
}
