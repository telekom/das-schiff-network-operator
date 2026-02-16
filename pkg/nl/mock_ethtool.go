package nl

// MockEthtool is a mock implementation of EthtoolInterface for testing.
type MockEthtool struct {
	ChangeFunc func(intf string, config map[string]bool) error
	Closed     bool
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
