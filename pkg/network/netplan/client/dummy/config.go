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

type DummyConfig struct {
	hint         string
	initialHints []string
	path         string
	initialState netplan.State
	plan         *netwrangler.Netplan
	log          *logrus.Entry
}

func newConfig(hint string, initialHints []string, directory string) (DummyConfig, error) {
	plan := netwrangler.New(&util.Layout{})
	log := logrus.WithField("path", directory).WithField("name", "dummy")
	result := DummyConfig{
		hint:         hint,
		initialHints: initialHints,
		path:         directory,
		log:          log,
		plan:         plan,
	}
	if _, err := os.OpenFile(filepath.Join(result.path, result.hint), os.O_CREATE, 0666); err != nil {
		return result, err
	}
	if state, err := result.load(); err != nil {
		return result, err
	} else {
		result.initialState = state
		result.Set(result.initialState)
	}
	return result, nil
}
func (config *DummyConfig) Reset() netplan.Error {
	config.log.Infof("cleaning existing configuration")
	config.plan = netwrangler.New(&util.Layout{})
	config.plan.BindMacs()
	return nil
}
func (config *DummyConfig) clear() error {
	// Clearing first the default hint if necessary (ignore errors because they're not important in this case)
	if err := filepath.Walk(config.path, func(path string, info fs.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		if slice.Find(config.initialHints, func(h string, i int) bool {
			return slice.Contains([]string{
				h,
				fmt.Sprintf("%s.yml", h),
				fmt.Sprintf("%s.yaml", h),
			}, info.Name())
		}) != nil {
			config.log.Infof("removing existing netplan file %s", path)
			os.Remove(path)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}
func (config *DummyConfig) Discard() netplan.Error {
	return nil
}

func (config *DummyConfig) Set(state netplan.State) netplan.Error {
	if planJson, err := json.Marshal(state); err != nil {
		return netplan.UnknownError{Err: err}
	} else {
		if err := json.Unmarshal(planJson, config.plan); err != nil {
			return netplan.UnknownError{Err: err}
		}
	}
	config.plan.BindMacs()
	return nil
}
func (config *DummyConfig) IsSynced() bool {
	newState, _ := config.Get()
	return config.initialState.Equals(newState)
}

func (config *DummyConfig) Apply() (applyErr netplan.Error) {
	if err := config.clear(); err != nil {
		return netplan.UnknownError{Err: err}
	}
	source := config.initialState
	if target, err := config.Get(); err != nil {
		return err
	} else {
		if virtualInterfacesToRemove, err := netplan.GetChangedVirtualInterfaces(source, target); err != nil {
			return netplan.ParseError(err)
		} else {
			if len(virtualInterfacesToRemove) > 0 {
				config.log.Warnf("removing existing links for virtual interfaces before netplan apply")
				for _, link := range virtualInterfacesToRemove {
					config.log.Warnf("would remove link %s if not dummy", link.Name)
				}
			}
		}
	}
	config.log.Infof("netplan configuration has changed. applying new state")
	if err := config.plan.Write(filepath.Join(config.path, config.hint)); err != nil {
		return netplan.UnknownError{Err: err}
	}
	return nil
}
func (client *Client) Generate() netplan.Error {
	return nil
}

func (config *DummyConfig) Get() (netplan.State, netplan.Error) {
	var result netplan.State
	if planJson, err := json.Marshal(config.plan); err != nil {
		return result, netplan.UnknownError{Err: err}
	} else {
		if err := json.Unmarshal(planJson, &result); err != nil {
			return result, netplan.UnknownError{Err: err}
		}
	}
	return result, nil

}
func (config *DummyConfig) load() (netplan.State, netplan.Error) {
	result := netplan.NewEmptyState()
	if err := filepath.Walk(config.path, func(path string, info fs.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		plan := netwrangler.New(&util.Layout{})
		if _, err := plan.Read(path, nil); err != nil {
			config.log.Warnf("error parsing netplan file %s; moving on. err: %s", path, err)
		}
		var partialState netplan.State
		if npJson, err := json.Marshal(plan); err != nil {
			return err
		} else {
			if err := json.Unmarshal(npJson, &partialState); err != nil {
				return err
			}
		}
		return result.Merge(&partialState)
	}); err != nil {
		return result, netplan.ParseError(err)
	}
	return result, nil
}
