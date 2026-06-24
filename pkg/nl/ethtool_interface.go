package nl

import (
	"fmt"

	"github.com/safchain/ethtool"
)

// EthtoolInterface defines the interface for ethtool operations.
type EthtoolInterface interface {
	Features(intf string) (map[string]bool, error)
	Change(intf string, config map[string]bool) error
	Close()
}

// ethtoolWrapper wraps the real ethtool to implement EthtoolInterface.
type ethtoolWrapper struct {
	eth *ethtool.Ethtool
}

func (e *ethtoolWrapper) Change(intf string, config map[string]bool) error {
	if err := e.eth.Change(intf, config); err != nil {
		return fmt.Errorf("ethtool change failed: %w", err)
	}
	return nil
}

func (e *ethtoolWrapper) Features(intf string) (map[string]bool, error) {
	features, err := e.eth.Features(intf)
	if err != nil {
		return nil, fmt.Errorf("ethtool features failed: %w", err)
	}
	return features, nil
}

func (e *ethtoolWrapper) Close() {
	e.eth.Close()
}

// newEthtoolFunc is a factory function for creating ethtool instances.
// It can be replaced in tests.
var newEthtoolFunc = func() (EthtoolInterface, error) {
	eth, err := ethtool.NewEthtool()
	if err != nil {
		return nil, fmt.Errorf("failed to create ethtool: %w", err)
	}
	return &ethtoolWrapper{eth: eth}, nil
}
