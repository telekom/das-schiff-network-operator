package nl

// MockEthtool is a mock implementation of EthtoolInterface for testing.
type MockEthtool struct {
	FeaturesFunc func(intf string) (map[string]bool, error)
	ChangeFunc   func(intf string, config map[string]bool) error
	Closed       bool
}

func (m *MockEthtool) Features(intf string) (map[string]bool, error) {
	if m.FeaturesFunc != nil {
		return m.FeaturesFunc(intf)
	}
	return map[string]bool{
		"rx-gro":                  true,
		"tx-generic-segmentation": true,
		"tx-tcp-segmentation":     true,
		"tx-tcp6-segmentation":    true,
	}, nil
}

func (m *MockEthtool) Change(intf string, config map[string]bool) error {
	if m.ChangeFunc != nil {
		return m.ChangeFunc(intf, config)
	}
	return nil
}

func (m *MockEthtool) Close() {
	m.Closed = true
}
