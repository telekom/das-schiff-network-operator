package dummy

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	netwrangler "github.com/rackn/netwrangler/netplan"
	"github.com/rackn/netwrangler/util"
	"github.com/sirupsen/logrus"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/slice"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
)

type Config struct {
	hint         string
	initialHints []string
	path         string
	initialState *netplan.State
	plan         *netwrangler.Netplan
	log          *logrus.Entry
}

func newConfig(hint string, initialHints []string, directory string) (Config, error) {
	plan := netwrangler.New(&util.Layout{})
	log := logrus.WithField("path", directory).WithField("name", "dummy")
	result := Config{
		hint:         hint,
		initialHints: initialHints,
		path:         directory,
		log:          log,
		plan:         plan,
	}
	p := filepath.Join(result.path, result.hint)
	if _, err := os.OpenFile(p, os.O_CREATE, 0o666); err != nil { //nolint:mnd
		return result, fmt.Errorf("failed to open file %s: %w", p, err)
	}
	state, err := result.load()
	if err != nil {
		return result, err
	}
	result.initialState = state
	if err := result.Set(result.initialState); err != nil {
		return result, err
	}
	return result, nil
}
func (config *Config) Reset() netplan.Error {
	config.log.Infof("cleaning existing configuration")
	config.plan = netwrangler.New(&util.Layout{})
	config.plan.BindMacs()
	return nil
}
func (config *Config) clear() error {
	// Clearing first the default hint if necessary (ignore errors because they're not important in this case)
	if err := filepath.Walk(config.path, func(path string, info fs.FileInfo, _ error) error {
		if info.IsDir() {
			return nil
		}
		if slice.Find(config.initialHints, func(h string, _ int) bool {
			return slice.Contains([]string{
				h,
				fmt.Sprintf("%s.yml", h),
				fmt.Sprintf("%s.yaml", h),
			}, info.Name())
		}) != nil {
			config.log.Infof("removing existing netplan file %s", path)
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("failed to remove file %s: %w", path, err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to remove netplan files: %w", err)
	}
	return nil
}
func (*Config) Discard() netplan.Error {
	return nil
}

func (config *Config) Set(state *netplan.State) netplan.Error {
	planJSON, err := json.Marshal(state)
	if err != nil {
		return netplan.UnknownError{Err: err}
	}
	if err := json.Unmarshal(planJSON, config.plan); err != nil {
		return netplan.UnknownError{Err: err}
	}
	config.plan.BindMacs()
	return nil
}
func (config *Config) IsSynced() bool {
	newState, _ := config.Get()
	return config.initialState.Equals(newState)
}

func (config *Config) Apply() (applyErr netplan.Error) {
	if err := config.clear(); err != nil {
		return netplan.UnknownError{Err: err}
	}
	source := config.initialState
	target, nperr := config.Get()
	if nperr != nil {
		return nperr
	}
	virtualInterfacesToRemove, err := netplan.GetChangedVirtualInterfaces(source, target)
	if err != nil {
		return netplan.ParseError(err)
	}
	if len(virtualInterfacesToRemove) > 0 {
		config.log.Warnf("removing existing links for virtual interfaces before netplan apply")
		for _, link := range virtualInterfacesToRemove {
			config.log.Warnf("would remove link %s if not dummy", link.Name)
		}
	}

	config.log.Infof("netplan configuration has changed. applying new state")
	if err := config.plan.Write(filepath.Join(config.path, config.hint)); err != nil {
		return netplan.UnknownError{Err: err}
	}
	return nil
}
func (*Client) Generate() netplan.Error {
	return nil
}

func (config *Config) Get() (*netplan.State, netplan.Error) {
	var result netplan.State
	planJSON, err := json.Marshal(config.plan)
	if err != nil {
		return nil, netplan.UnknownError{Err: err}
	}
	if err := json.Unmarshal(planJSON, &result); err != nil {
		return nil, netplan.UnknownError{Err: err}
	}

	return &result, nil
}

func (config *Config) load() (*netplan.State, netplan.Error) {
	result := netplan.NewEmptyState()
	if err := filepath.Walk(config.path, func(path string, info fs.FileInfo, _ error) error {
		if info.IsDir() {
			return nil
		}
		plan := netwrangler.New(&util.Layout{})
		if _, err := plan.Read(path, nil); err != nil {
			config.log.Warnf("error parsing netplan file %s; moving on. err: %s", path, err)
		}
		var partialState netplan.State
		if npJSON, err := json.Marshal(plan); err != nil {
			return fmt.Errorf("failed to masrhal JSON: %w", err)
		} else {
			if err := json.Unmarshal(npJSON, &partialState); err != nil {
				return fmt.Errorf("failed to unmarshal JSON: %w", err)
			}
		}
		return result.Merge(&partialState)
	}); err != nil {
		return nil, netplan.ParseError(err)
	}
	return &result, nil
}
