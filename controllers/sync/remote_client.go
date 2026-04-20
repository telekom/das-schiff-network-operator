package sync

import (
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RemoteClientConfig holds tunables applied to every remote workload-cluster client
// constructed by RemoteClientManager. Zero values are replaced by defaults at
// construction time via NewRemoteClientManager.
type RemoteClientConfig struct {
	// QPS is the maximum requests-per-second allowed by the rest.Config rate limiter.
	QPS float32
	// Burst is the maximum burst size allowed by the rest.Config rate limiter.
	Burst int
	// Timeout is the per-request timeout applied to the remote rest.Config.
	// A zero value means no timeout (the controller-runtime default).
	Timeout time.Duration
}

// Default values used when a field of RemoteClientConfig is left at its zero value.
const (
	DefaultRemoteClientQPS     float32       = 50
	DefaultRemoteClientBurst   int           = 100
	DefaultRemoteClientTimeout time.Duration = 30 * time.Second
)

func (c RemoteClientConfig) withDefaults() RemoteClientConfig {
	if c.QPS == 0 {
		c.QPS = DefaultRemoteClientQPS
	}
	if c.Burst == 0 {
		c.Burst = DefaultRemoteClientBurst
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultRemoteClientTimeout
	}
	return c
}

// RemoteClientManager maintains a thread-safe map of cluster (namespace/name) → remote client.Client.
type RemoteClientManager struct {
	mu      sync.RWMutex
	clients map[types.NamespacedName]client.Client
	scheme  *runtime.Scheme
	cfg     RemoteClientConfig
}

// NewRemoteClientManager creates a new manager with the given remote-client tunables.
// Any zero-valued fields in cfg are replaced with the package defaults.
func NewRemoteClientManager(scheme *runtime.Scheme, cfg RemoteClientConfig) *RemoteClientManager {
	return &RemoteClientManager{
		clients: make(map[types.NamespacedName]client.Client),
		scheme:  scheme,
		cfg:     cfg.withDefaults(),
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

// UpdateFromKubeconfig parses raw kubeconfig bytes and creates/replaces the cached client.
func (m *RemoteClientManager) UpdateFromKubeconfig(key types.NamespacedName, kubeconfig []byte) error {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("parsing kubeconfig for %q: %w", key, err)
	}
	cfg.QPS = m.cfg.QPS
	cfg.Burst = m.cfg.Burst
	cfg.Timeout = m.cfg.Timeout

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
