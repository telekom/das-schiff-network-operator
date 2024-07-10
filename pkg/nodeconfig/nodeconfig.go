package nodeconfig

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:generate mockgen -destination ./mock/mock_nodeconfig.go . ConfigInterface
type ConfigInterface interface {
	CrateInvalid(ctx context.Context, c client.Client) error
	CreateBackup(ctx context.Context, c client.Client) error
	Deploy(ctx context.Context, c client.Client, logger logr.Logger, invalidationTimeout time.Duration) error
	GetActive() bool
	GetCancelFunc() *context.CancelFunc
	GetCurrentConfigStatus() string
	GetDeployed() bool
	GetInvalid() *v1alpha1.NodeConfig
	GetName() string
	GetNext() *v1alpha1.NodeConfig
	Prune(ctx context.Context, c client.Client) error
	SetActive(value bool)
	SetBackupAsNext() bool
	SetCancelFunc(f *context.CancelFunc)
	SetDeployed(value bool)
	UpdateNext(next *v1alpha1.NodeConfig)
}

const (
	StatusProvisioning = "provisioning"
	StatusInvalid      = "invalid"
	StatusProvisioned  = "provisioned"
	statusEmpty        = ""

	DefaultNodeUpdateLimit = 1
	defaultCooldownTime    = 100 * time.Millisecond

	InvalidSuffix = "-invalid"
	BackupSuffix  = "-backup"

	ParentCtx contextKey = "parentCtx"
)

type Config struct {
	name       string
	current    *v1alpha1.NodeConfig
	next       *v1alpha1.NodeConfig
	backup     *v1alpha1.NodeConfig
	invalid    *v1alpha1.NodeConfig
	mtx        sync.RWMutex
	active     atomic.Bool
	deployed   atomic.Bool
	cancelFunc atomic.Pointer[context.CancelFunc]
}

type contextKey string

func New(name string, current, backup, invalid *v1alpha1.NodeConfig) *Config {
	nc := NewEmpty(name)
	nc.current = current
	nc.backup = backup
	nc.invalid = invalid
	return nc
}

func NewEmpty(name string) *Config {
	nc := &Config{
		name:    name,
		current: v1alpha1.NewEmptyConfig(name),
	}
	nc.active.Store(true)
	return nc
}

func (nc *Config) SetCancelFunc(f *context.CancelFunc) {
	nc.cancelFunc.Store(f)
}

func (nc *Config) GetCancelFunc() *context.CancelFunc {
	return nc.cancelFunc.Load()
}

func (nc *Config) GetName() string {
	nc.mtx.RLock()
	defer nc.mtx.RUnlock()
	return nc.name
}

func (nc *Config) SetActive(value bool) {
	nc.active.Store(value)
}

func (nc *Config) GetActive() bool {
	return nc.active.Load()
}

func (nc *Config) SetDeployed(value bool) {
	nc.mtx.Lock()
	defer nc.mtx.Unlock()
	nc.deployed.Store(value)
}

func (nc *Config) GetDeployed() bool {
	nc.mtx.RLock()
	defer nc.mtx.RUnlock()
	return nc.deployed.Load()
}

func (nc *Config) GetNext() *v1alpha1.NodeConfig {
	nc.mtx.RLock()
	defer nc.mtx.RUnlock()
	return nc.next
}

func (nc *Config) GetInvalid() *v1alpha1.NodeConfig {
	nc.mtx.RLock()
	defer nc.mtx.RUnlock()
	return nc.invalid
}

func (nc *Config) GetCurrentConfigStatus() string {
	nc.mtx.RLock()
	defer nc.mtx.RUnlock()
	return nc.current.Status.ConfigStatus
}

func (nc *Config) UpdateNext(next *v1alpha1.NodeConfig) {
	nc.mtx.Lock()
	defer nc.mtx.Unlock()
	if nc.next == nil {
		nc.next = v1alpha1.NewEmptyConfig(nc.name)
	}
	v1alpha1.CopyNodeConfig(next, nc.next, nc.name)
}

func (nc *Config) Deploy(ctx context.Context, c client.Client, logger logr.Logger, invalidationTimeout time.Duration) error {
	skip, err := nc.deployNext(ctx, c, logger)
	if err != nil {
		return fmt.Errorf("error creating API objects: %w", err)
	}

	// either node was deleted or new config equals current config - skip
	if skip {
		return nil
	}

	if err := nc.waitForConfig(ctx, c, nc.current, statusEmpty, false, logger, false, invalidationTimeout); err != nil {
		return fmt.Errorf("error waiting for config %s with status %s: %w", nc.name, statusEmpty, err)
	}

	if err := nc.updateStatus(ctx, c, nc.current, StatusProvisioning); err != nil {
		return fmt.Errorf("error updating status of config %s to %s: %w", nc.name, StatusProvisioning, err)
	}

	if err := nc.waitForConfig(ctx, c, nc.current, StatusProvisioning, false, logger, false, invalidationTimeout); err != nil {
		return fmt.Errorf("error waiting for config %s with status %s: %w", nc.name, StatusProvisioning, err)
	}

	if err := nc.waitForConfig(ctx, c, nc.current, StatusProvisioned, true, logger, true, invalidationTimeout); err != nil {
		return fmt.Errorf("error waiting for config %s with status %s: %w", nc.name, StatusProvisioned, err)
	}

	if err := nc.CreateBackup(ctx, c); err != nil {
		return fmt.Errorf("error creating backup for %s: %w", nc.name, err)
	}

	return nil
}

func (nc *Config) deployNext(ctx context.Context, c client.Client, logger logr.Logger) (bool, error) {
	nc.mtx.Lock()
	defer nc.mtx.Unlock()

	if nc.next == nil {
		nc.next = v1alpha1.NewEmptyConfig(nc.name)
	}

	if !nc.active.Load() {
		return true, nil
	}

	if nc.current == nil {
		nc.current = v1alpha1.NewEmptyConfig(nc.name)
	}

	skip, err := createOrUpdate(ctx, c, nc.current, nc.next, logger)
	if err != nil {
		return false, fmt.Errorf("error configuring node config object: %w", err)
	}

	if skip {
		return true, nil
	}

	nc.deployed.Store(true)

	return false, nil
}

func createOrUpdate(ctx context.Context, c client.Client, current, next *v1alpha1.NodeConfig, logger logr.Logger) (bool, error) {
	if err := c.Get(ctx, client.ObjectKeyFromObject(current), current); err != nil && apierrors.IsNotFound(err) {
		v1alpha1.CopyNodeConfig(next, current, current.Name)
		// config does not exist - create
		if err := c.Create(ctx, current); err != nil {
			return false, fmt.Errorf("error creating NodeConfig object: %w", err)
		}
	} else if err != nil {
		return false, fmt.Errorf("error getting current config: %w", err)
	} else {
		// config already exists - update
		// check if new config is equal to existing config
		// if so, skip the update as nothing has to be updated
		if next.IsEqual(current) {
			logger.Info("new config is equal to current config, skipping...", "config", current.Name)
			return true, nil
		}
		v1alpha1.CopyNodeConfig(next, current, current.Name)
		if err := updateConfig(ctx, c, current); err != nil {
			return false, fmt.Errorf("error updating NodeConfig object: %w", err)
		}
	}
	return false, nil
}

func (nc *Config) SetBackupAsNext() bool {
	nc.mtx.Lock()
	defer nc.mtx.Unlock()
	if nc.backup != nil {
		if nc.next == nil {
			nc.next = v1alpha1.NewEmptyConfig(nc.current.Name)
		}
		v1alpha1.CopyNodeConfig(nc.backup, nc.next, nc.current.Name)
		return true
	}
	return false
}

func (nc *Config) CreateBackup(ctx context.Context, c client.Client) error {
	backupName := nc.name + BackupSuffix
	createNew := false
	if nc.backup == nil {
		nc.backup = v1alpha1.NewEmptyConfig(backupName)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: backupName}, nc.backup); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("error getting backup config %s: %w", backupName, err)
		}
		createNew = true
	}

	if nc.current != nil {
		v1alpha1.CopyNodeConfig(nc.current, nc.backup, backupName)
	} else {
		v1alpha1.CopyNodeConfig(v1alpha1.NewEmptyConfig(backupName), nc.backup, backupName)
	}

	if createNew {
		if err := c.Create(ctx, nc.backup); err != nil {
			return fmt.Errorf("error creating backup config: %w", err)
		}
	} else {
		if err := c.Update(ctx, nc.backup); err != nil {
			return fmt.Errorf("error updating backup config: %w", err)
		}
	}

	return nil
}

func (nc *Config) CrateInvalid(ctx context.Context, c client.Client) error {
	nc.mtx.Lock()
	defer nc.mtx.Unlock()
	invalidName := fmt.Sprintf("%s%s", nc.name, InvalidSuffix)

	if nc.invalid == nil {
		nc.invalid = v1alpha1.NewEmptyConfig(invalidName)
	}

	if err := c.Get(ctx, types.NamespacedName{Name: invalidName}, nc.invalid); err != nil {
		if apierrors.IsNotFound(err) {
			// invalid config for the node does not exist - create new
			v1alpha1.CopyNodeConfig(nc.current, nc.invalid, invalidName)
			if err = c.Create(ctx, nc.invalid); err != nil {
				return fmt.Errorf("cannot store invalid config for node %s: %w", nc.name, err)
			}
			return nil
		}
		// other kind of error occurred - abort
		return fmt.Errorf("error getting invalid config for node %s: %w", nc.name, err)
	}

	// invalid config for the node exist - update
	v1alpha1.CopyNodeConfig(nc.current, nc.invalid, invalidName)
	if err := updateConfig(ctx, c, nc.invalid); err != nil {
		return fmt.Errorf("error updating invalid config for node %s: %w", nc.name, err)
	}

	return nil
}

func (nc *Config) Prune(ctx context.Context, c client.Client) error {
	nc.mtx.Lock()
	defer nc.mtx.Unlock()

	if nc.current != nil {
		if err := c.Delete(ctx, nc.current); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("error deleting current config: %w", err)
		}
	}
	if nc.backup != nil {
		if err := c.Delete(ctx, nc.backup); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("error deleting backup config: %w", err)
		}
	}
	if nc.invalid != nil {
		if err := c.Delete(ctx, nc.invalid); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("error deleting invalid config: %w", err)
		}
	}
	return nil
}

func (nc *Config) waitForConfig(ctx context.Context, c client.Client, config *v1alpha1.NodeConfig,
	expectedStatus string, failIfInvalid bool, logger logr.Logger, invalidate bool, invalidationTimeout time.Duration) error {
	for {
		select {
		case <-ctx.Done():
			return nc.handleContextDone(ctx, c, config, logger, invalidate, invalidationTimeout)
		default:
			if err := nc.apiUpdate(ctx, c, config); err != nil {
				return fmt.Errorf("error updating API boject: %w", err)
			}
			// return no error if accepting any status (""), expected status, or if node was deleted
			if expectedStatus == "" || config.Status.ConfigStatus == expectedStatus || !nc.active.Load() {
				return nil
			}

			// return error if status is invalid
			if failIfInvalid && config.Status.ConfigStatus == StatusInvalid {
				return fmt.Errorf("error creating NodeConfig - node %s reported state as %s", config.Name, config.Status.ConfigStatus)
			}
			time.Sleep(defaultCooldownTime)
		}
	}
}

func (nc *Config) handleContextDone(ctx context.Context, c client.Client, config *v1alpha1.NodeConfig,
	logger logr.Logger, invalidate bool, invalidationTimeout time.Duration) error {
	// contex cancelled means that node was removed
	// don't report error here
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) && invalidate {
		if err := nc.handleContextDeadline(ctx, c, invalidationTimeout, config, logger); err != nil {
			return fmt.Errorf("error while handling config invalidation: %w", err)
		}
		return fmt.Errorf("context timeout: %w", ctx.Err())
	}
	// return error if there was different error than cancel
	return fmt.Errorf("context error: %w", ctx.Err())
}

func (nc *Config) apiUpdate(ctx context.Context, c client.Client, config *v1alpha1.NodeConfig) error {
	nc.mtx.Lock()
	defer nc.mtx.Unlock()
	if err := c.Get(ctx, types.NamespacedName{Name: config.Name, Namespace: config.Namespace}, config); err != nil {
		if apierrors.IsNotFound(err) {
			// discard eror - node was deleted
			return nil
		}
		return fmt.Errorf("error getting config %s from APi server: %w", config.Name, err)
	}
	return nil
}

// old context exceeded deadline so new config is created from the parent
// nolint: contextcheck
func (nc *Config) handleContextDeadline(ctx context.Context, c client.Client, invalidationTimeout time.Duration, config *v1alpha1.NodeConfig, logger logr.Logger) error {
	pCtx, ok := ctx.Value(ParentCtx).(context.Context)
	if !ok {
		return fmt.Errorf("error getting parent context")
	}
	statusCtx, statusCancel := context.WithTimeout(pCtx, invalidationTimeout)
	defer statusCancel()

	if err := nc.updateStatus(statusCtx, c, config, StatusInvalid); err != nil {
		return fmt.Errorf("error setting config %s status %s: %w", config.GetName(), StatusInvalid, err)
	}

	if err := nc.waitForConfig(statusCtx, c, config, StatusInvalid, false, logger, false, invalidationTimeout); err != nil {
		return fmt.Errorf("error waiting for config %s status %s: %w", config.GetName(), StatusInvalid, err)
	}
	return nil
}

func (nc *Config) updateStatus(ctx context.Context, c client.Client, config *v1alpha1.NodeConfig, status string) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("status update error: %w", ctx.Err())
		default:
			nc.mtx.Lock()
			config.Status.ConfigStatus = status
			err := c.Status().Update(ctx, config)
			nc.mtx.Unlock()
			if err != nil {
				if apierrors.IsConflict(err) {
					// if there is a conflict, update local copy of the config
					nc.mtx.Lock()
					if getErr := c.Get(ctx, client.ObjectKeyFromObject(config), config); getErr != nil {
						nc.mtx.Unlock()
						return fmt.Errorf("error updating status: %w", getErr)
					}
					nc.mtx.Unlock()
					time.Sleep(defaultCooldownTime)
					continue
				}
				return fmt.Errorf("status update error: %w", err)
			} else {
				return nil
			}
		}
	}
}

func updateConfig(ctx context.Context, c client.Client, config *v1alpha1.NodeConfig) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("config update error (context): %w", ctx.Err())
		default:
			if err := c.Update(ctx, config); err != nil {
				if apierrors.IsConflict(err) {
					// if there is a conflict, update local copy of the config
					if err := c.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
						return fmt.Errorf("config update error (conflict): %w", err)
					}
					time.Sleep(defaultCooldownTime)
					continue
				}
				return fmt.Errorf("config update error (error): %w", err)
			} else {
				return nil
			}
		}
	}
}
