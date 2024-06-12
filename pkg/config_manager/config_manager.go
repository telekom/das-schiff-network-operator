package configmanager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	configmap "github.com/telekom/das-schiff-network-operator/pkg/config_map"
	"github.com/telekom/das-schiff-network-operator/pkg/nodeconfig"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler"
	"golang.org/x/sync/semaphore"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultCooldownTime = time.Millisecond * 100
)

type ConfigManager struct {
	client       client.Client
	configsMap   configmap.Interface
	cr           reconciler.ConfigReconcilerInterface
	nr           reconciler.NodeReconcilerInterface
	changes      chan bool
	deletedNodes chan []string
	logger       logr.Logger
	timeout      time.Duration
	sem          semaphore.Weighted
}

func New(c client.Client, cr reconciler.ConfigReconcilerInterface, nr reconciler.NodeReconcilerInterface, log logr.Logger,
	timeout time.Duration, limit int64, changes chan bool, deleteNodes chan []string) *ConfigManager {
	// disable gradual rolllout if limit is < 1
	if limit < 1 {
		limit = math.MaxInt64
	}
	return &ConfigManager{
		client:       c,
		configsMap:   &configmap.ConfigMap{},
		cr:           cr,
		nr:           nr,
		logger:       log,
		changes:      changes,
		deletedNodes: deleteNodes,
		timeout:      timeout,
		sem:          *semaphore.NewWeighted(limit),
	}
}

// WatchConfigs waits for cm.deletedNodes channel.
func (cm *ConfigManager) WatchDeletedNodes(ctx context.Context, errCh chan error) {
	cm.logger.Info("starting watching for deleted nodes...")
	for {
		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.Canceled) {
				errCh <- fmt.Errorf("error watching configs: %w", ctx.Err())
			} else {
				errCh <- nil
			}
			return
		case nodes := <-cm.deletedNodes:
			cm.logger.Info("nodes deleted", "nodes", nodes)
			for _, n := range nodes {
				config, err := cm.configsMap.Get(n)
				if err != nil {
					cm.logger.Error(err, "error getting config", "node", n)
					continue
				}

				if config == nil {
					cm.logger.Info("no in-memory config found", "node", n)
					continue
				}

				cm.configsMap.Delete(n)
				config.SetActive(false)
				cancel := config.GetCancelFunc()
				if cancel != nil {
					(*cancel)()
				}
			}
		default:
			time.Sleep(defaultCooldownTime)
		}
	}
}

// WatchConfigs waits for cm.changes channel.
func (cm *ConfigManager) WatchConfigs(ctx context.Context, errCh chan error) {
	cm.logger.Info("starting watching for changes...")
	for {
		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.Canceled) {
				errCh <- fmt.Errorf("error watching configs: %w", ctx.Err())
			} else {
				errCh <- nil
			}
			return
		case <-cm.changes:
			cm.logger.Info("got notification about changes")
			err := cm.updateConfigs()
			if err != nil {
				errCh <- fmt.Errorf("error updating configs: %w", err)
				return
			}
			err = cm.deployConfigs(ctx)
			if err != nil {
				if err := cm.restoreBackup(ctx); err != nil {
					cm.logger.Error(err, "error restoring backup")
				}
			}
		default:
			time.Sleep(defaultCooldownTime)
		}
	}
}

// DirtyStartup will load all previously deployed NodeConfigs into current leader.
func (cm *ConfigManager) DirtyStartup(ctx context.Context) error {
	// get all known backup data and load it into config manager memory
	if err := cm.loadConfigs(ctx); err != nil {
		return fmt.Errorf("error loading configs: %w", err)
	}

	var isDirty bool
	var err error
	// check previous leader's work
	if isDirty, err = cm.isDirty(); err != nil {
		return fmt.Errorf("error checking dirty state: %w", err)
	}

	// previous leader left cluster in dirty state - restorte backup
	if isDirty {
		if err := cm.restoreBackup(ctx); err != nil {
			return fmt.Errorf("error restoring backup: %w", err)
		}
	}
	return nil
}

func (cm *ConfigManager) isDirty() (bool, error) {
	isDirty := false
	var err error
	cfg, err := cm.configsMap.GetSlice()
	if err != nil {
		return false, fmt.Errorf("error getting config slice: %w", err)
	}

	for _, c := range cfg {
		current := c.GetCurrent()
		if c.GetInvalid() != nil || (current != nil && current.Status.ConfigStatus != nodeconfig.StatusProvisioned) {
			return true, nil
		}
	}

	return isDirty, nil
}

func (cm *ConfigManager) updateConfigs() error {
	cm.logger.Info("updating configs...")
	currentNodes := cm.nr.GetNodes()
	for name := range currentNodes {
		n := currentNodes[name]
		next, err := cm.cr.CreateConfigForNode(name, n)
		if err != nil {
			return fmt.Errorf("error creating config for the node %s: %w", name, err)
		}
		cfg, err := cm.configsMap.Get(name)
		if err != nil {
			return fmt.Errorf("error getting config for node %s: %w", name, err)
		}
		if cfg != nil {
			cfg.UpdateNext(next)
		} else {
			cfg = nodeconfig.NewEmpty(name)
			cfg.UpdateNext(next)
			cm.configsMap.Store(name, cfg)
		}
	}
	return nil
}

func (cm *ConfigManager) deploy(ctx context.Context, configs []nodeconfig.ConfigInterface) error {
	for _, cfg := range configs {
		cfg.SetDeployed(false)
	}

	if err := cm.validateConfigs(configs); err != nil {
		return fmt.Errorf("error validating configs: %w", err)
	}

	deploymentCtx, deploymentCancel := context.WithCancel(ctx)
	defer deploymentCancel()

	wg := &sync.WaitGroup{}
	errCh := make(chan error, len(configs))
	for _, cfg := range configs {
		wg.Add(1)
		go func(config nodeconfig.ConfigInterface) {
			defer wg.Done()

			if err := cm.sem.Acquire(ctx, 1); err != nil {
				errCh <- fmt.Errorf("error acquring semaphore: %w", err)
				return
			}
			defer cm.sem.Release(1)

			select {
			case <-deploymentCtx.Done():
				errCh <- deploymentCtx.Err()
				return
			default:
				err := cm.deployConfig(deploymentCtx, config)
				if err != nil {
					deploymentCancel()
				}
				errCh <- err
				return
			}
		}(cfg)
	}

	wg.Wait()
	close(errCh)

	if err := cm.checkErrors(errCh); err != nil {
		return fmt.Errorf("errors occurred: %w", err)
	}

	return nil
}

func (cm *ConfigManager) checkErrors(errCh chan error) error {
	errCnt := 0
	var firstErr error
	for err := range errCh {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if !errors.Is(err, context.Canceled) {
				cm.logger.Error(err, "depoyment error")
			}
			errCnt++
		}
	}

	if errCnt > 0 {
		return fmt.Errorf("%d error(s) occurred while processing configs, please check the logs for details: first known error: %w", errCnt, firstErr)
	}

	return nil
}

// nolint: contextcheck
func (cm *ConfigManager) deployConfig(ctx context.Context, cfg nodeconfig.ConfigInterface) error {
	if cfg.GetActive() {
		cfgContext, cfgCancel := context.WithTimeout(ctx, cm.timeout)
		cfgContext = context.WithValue(cfgContext, nodeconfig.ParentCtx, ctx)
		cfg.SetCancelFunc(&cfgCancel)

		cm.logger.Info("processing config", "name", cfg.GetName())
		if err := cfg.Deploy(cfgContext, cm.client, cm.logger, cm.timeout); err != nil {
			// we invalidate config on new, separate context, so invalidadion won't get cancelled
			// if node update limit is set to more than 1.
			invalidationCtx, invalidationCancel := context.WithTimeout(context.Background(), cm.timeout)
			defer invalidationCancel()
			if err := cfg.CrateInvalid(invalidationCtx, cm.client); err != nil {
				return fmt.Errorf("error creating invalid config object: %w", err)
			}
			return fmt.Errorf("error deploying config %s: %w", cfg.GetName(), err)
		}
		if err := cfg.DeleteInvalid(ctx, cm.client); err != nil {
			return fmt.Errorf("error deleting invalid config: %w", err)
		}
		cm.logger.Info("deployed", "name", cfg.GetName())
	}
	return nil
}

func (cm *ConfigManager) validateConfigs(configs []nodeconfig.ConfigInterface) error {
	cm.logger.Info("validating configs...")
	for _, cfg := range configs {
		if !cfg.GetActive() {
			continue
		}

		next := cfg.GetNext()
		invalid := cfg.GetInvalid()

		if invalid != nil && next != nil {
			if next.IsEqual(invalid) {
				return fmt.Errorf("config for node %s results in invalid config", cfg.GetName())
			}
		}
	}
	return nil
}

func (cm *ConfigManager) deployConfigs(ctx context.Context) error {
	cm.logger.Info("deploying configs ...")
	toDeploy, err := cm.configsMap.GetSlice()
	if err != nil {
		return fmt.Errorf("error converting config map to slice: %w", err)
	}

	if err := cm.deploy(ctx, toDeploy); err != nil {
		return fmt.Errorf("error deploying configs: %w", err)
	}

	return nil
}

func (cm *ConfigManager) restoreBackup(ctx context.Context) error {
	cm.logger.Info("restoring backup...")
	slice, err := cm.configsMap.GetSlice()
	if err != nil {
		return fmt.Errorf("error converting config map to slice: %w", err)
	}
	toDeploy := []nodeconfig.ConfigInterface{}
	for _, cfg := range slice {
		if cfg.GetDeployed() {
			if backupAvailable := cfg.SetBackupAsNext(); backupAvailable {
				toDeploy = append(toDeploy, cfg)
			}
		}
	}

	if err := cm.deploy(ctx, toDeploy); err != nil {
		return fmt.Errorf("error deploying configs: %w", err)
	}

	cm.logger.Info("backup restored")
	return nil
}

func (cm *ConfigManager) loadConfigs(ctx context.Context) error {
	// get all known backup data and load it into config manager memory
	nodes, err := reconciler.ListNodes(ctx, cm.client)
	if err != nil {
		return fmt.Errorf("error listing nodes: %w", err)
	}

	knownConfigs := &v1alpha1.NodeConfigList{}
	if err := cm.client.List(ctx, knownConfigs); err != nil {
		return fmt.Errorf("error listing NodeConfigs: %w", err)
	}

	cm.createConfigsFromBackup(nodes, knownConfigs)

	return nil
}

func (cm *ConfigManager) createConfigsFromBackup(nodes map[string]*corev1.Node, knownConfigs *v1alpha1.NodeConfigList) {
	for _, node := range nodes {
		current, backup, invalid := cm.matchConfigs(knownConfigs, node)
		cfg := nodeconfig.New(node.Name, current, backup, invalid)
		if backup != nil {
			cfg.SetDeployed(true)
		}
		cm.configsMap.Store(node.Name, cfg)
	}
}

func (cm *ConfigManager) matchConfigs(knownConfigs *v1alpha1.NodeConfigList, node *corev1.Node) (current, backup, invalid *v1alpha1.NodeConfig) {
	for i := range knownConfigs.Items {
		for j := range knownConfigs.Items[i].OwnerReferences {
			if knownConfigs.Items[i].OwnerReferences[j].UID == node.UID {
				if knownConfigs.Items[i].Name == node.Name {
					cm.logger.Info("found current config", "node", node.Name, "status", knownConfigs.Items[i].Status.ConfigStatus)

					current = &knownConfigs.Items[i]
				}
				if strings.Contains(knownConfigs.Items[i].Name, nodeconfig.InvalidSuffix) {
					cm.logger.Info("found invalid config", "node", node.Name)
					invalid = &knownConfigs.Items[i]
				}
				if strings.Contains(knownConfigs.Items[i].Name, nodeconfig.BackupSuffix) {
					cm.logger.Info("found backup config", "node", node.Name)
					backup = &knownConfigs.Items[i]
				}
			}
		}
	}
	return current, backup, invalid
}
