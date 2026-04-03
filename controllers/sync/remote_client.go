package sync

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RemoteClientManager maintains a thread-safe map of namespace → remote client.Client.
type RemoteClientManager struct {
	mu      sync.RWMutex
	clients map[string]client.Client
	scheme  *runtime.Scheme
}

// NewRemoteClientManager creates a new manager.
func NewRemoteClientManager(scheme *runtime.Scheme) *RemoteClientManager {
	return &RemoteClientManager{
		clients: make(map[string]client.Client),
		scheme:  scheme,
	}
}

// Get returns the cached client for a namespace, or nil if none exists.
func (m *RemoteClientManager) Get(namespace string) client.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[namespace]
}

// UpdateFromKubeconfig parses raw kubeconfig bytes and creates/replaces the cached client.
func (m *RemoteClientManager) UpdateFromKubeconfig(namespace string, kubeconfig []byte) error {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("parsing kubeconfig for %q: %w", namespace, err)
	}
	cfg.QPS = 20
	cfg.Burst = 30

	return m.updateFromConfig(namespace, cfg)
}

func (m *RemoteClientManager) updateFromConfig(namespace string, cfg *rest.Config) error {
	c, err := client.New(cfg, client.Options{Scheme: m.scheme})
	if err != nil {
		return fmt.Errorf("building client for %q: %w", namespace, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[namespace] = c
	return nil
}

// Remove tears down the cached client for a namespace.
func (m *RemoteClientManager) Remove(namespace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, namespace)
}

// Has returns true if a client exists for the namespace.
func (m *RemoteClientManager) Has(namespace string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.clients[namespace]
	return ok
}
