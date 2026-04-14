package sync

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RemoteClientManager maintains a thread-safe map of cluster (namespace/name) → remote client.Client.
type RemoteClientManager struct {
	mu      sync.RWMutex
	clients map[types.NamespacedName]client.Client
	scheme  *runtime.Scheme
}

// NewRemoteClientManager creates a new manager.
func NewRemoteClientManager(scheme *runtime.Scheme) *RemoteClientManager {
	return &RemoteClientManager{
		clients: make(map[types.NamespacedName]client.Client),
		scheme:  scheme,
	}
}

// Get returns the cached client for a cluster, or nil if none exists.
func (m *RemoteClientManager) Get(key types.NamespacedName) client.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[key]
}

// GetByNamespace returns all cached clients whose key matches the given namespace.
func (m *RemoteClientManager) GetByNamespace(namespace string) []client.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []client.Client
	for k, c := range m.clients {
		if k.Namespace == namespace {
			result = append(result, c)
		}
	}
	return result
}

const (
	remoteClientQPS   = 50
	remoteClientBurst = 100
)

// UpdateFromKubeconfig parses raw kubeconfig bytes and creates/replaces the cached client.
func (m *RemoteClientManager) UpdateFromKubeconfig(key types.NamespacedName, kubeconfig []byte) error {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("parsing kubeconfig for %q: %w", key, err)
	}
	cfg.QPS = remoteClientQPS
	cfg.Burst = remoteClientBurst

	return m.updateFromConfig(key, cfg)
}

func (m *RemoteClientManager) updateFromConfig(key types.NamespacedName, cfg *rest.Config) error {
	c, err := client.New(cfg, client.Options{Scheme: m.scheme})
	if err != nil {
		return fmt.Errorf("building client for %q: %w", key, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[key] = c
	return nil
}

// Remove tears down the cached client for a cluster.
func (m *RemoteClientManager) Remove(key types.NamespacedName) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, key)
}

// Has returns true if a client exists for the cluster.
func (m *RemoteClientManager) Has(key types.NamespacedName) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.clients[key]
	return ok
}
