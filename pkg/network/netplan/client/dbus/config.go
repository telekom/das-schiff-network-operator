package dbus

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/sirupsen/logrus"
	"github.com/telekom/das-schiff-network-operator/pkg/network/net"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	ConfigGetCall    = "io.netplan.Netplan.Config.Get"
	ConfigTryCall    = "io.netplan.Netplan.Config.Try"
	ConfigSetCall    = "io.netplan.Netplan.Config.Set"
	ConfigApplyCall  = "io.netplan.Netplan.Config.Apply"
	ConfigCancelCall = "io.netplan.Netplan.Config.Cancel"

	ConfigInterfacePath = "io.netplan.Netplan.Config"
)
const ApplyAttempts = 3

type Config struct {
	hint         string
	initialHints []string
	path         string
	conn         *dbus.Conn
	initialState *netplan.State
	log          *logrus.Entry
	netManager   net.Manager
	executionLog *logrus.Logger
}

func newConfig(hint string, initialHints []string, conn *dbus.Conn, netManager net.Manager, executionLog *logrus.Logger) (Config, error) {
	object := conn.Object(InterfacePath, ObjectPath)
	executionLog.Infof("busctl --system call io.netplan.Netplan /io/netplan/Netplan io.netplan.Netplan Config")
	call := object.Call(ConfigCall, 0)
	if call.Err != nil {
		return Config{}, netplan.ParseError(call.Err)
	}
	var path dbus.ObjectPath
	if err := call.Store(&path); err != nil {
		return Config{}, netplan.ParseError(err)
	}
	result := Config{
		hint:         hint,
		initialHints: initialHints,
		path:         string(path),
		conn:         conn,
		log:          logrus.WithField("path", path),
		executionLog: executionLog,
		netManager:   netManager,
	}
	state, err := result.Get()
	if err != nil {
		return result, err
	}
	result.initialState = state

	return result, nil
}

func (config *Config) Reset() netplan.Error {
	config.log.Infof("cleaning existing configuration")
	// Clearing first the default hint if necessary (ignore errors because they're not important in this case)
	for _, hint := range config.initialHints {
		if err := config.setProperty(hint, "network", nil); err != nil {
			return netplan.ParseError(err)
		}
	}
	return netplan.ParseError(config.setProperty(config.hint, "network", nil))
	// if current, err := config.Get(); err != nil {
	// 	return err
	// } else {
	//   iter := current.DeviceIterator()
	// 	for iter.HasNext() {
	// 		item := iter.Next()
	// 		item.Device.Clear()
	// 		iter.Apply(item)
	// 	}
	// 	// Clearing first the default hint if necessary (ignore errors because they're not important in this case)
	// 	config.set(current, true)

	// 	return config.set(current, false)
	// }
}

func (config *Config) Discard() netplan.Error {
	configObject := config.conn.Object(InterfacePath, dbus.ObjectPath(config.path))
	config.executionLog.Infof("busctl --system call io.netplan.Netplan %s io.netplan.Netplan.Config Cancel", config.path)
	cancelCall := configObject.Call(ConfigCancelCall, 0)

	if cancelCall.Err != nil {
		return netplan.ParseError(cancelCall.Err)
	}
	return nil
}

func (config *Config) setProperty(hint, path string, value interface{}) error {
	delta := path + "="
	if value == nil {
		delta += "NULL"
	} else {
		if jsonValue, err := json.Marshal(value); err == nil {
			delta += string(jsonValue)
		} else {
			delta += fmt.Sprint(value)
		}
	}

	configObject := config.conn.Object(InterfacePath, dbus.ObjectPath(config.path))
	config.log.Debugf("settings delta for hint %s: %s ", hint, delta)
	config.executionLog.Infof("busctl --system call io.netplan.Netplan %s io.netplan.Netplan.Config Set ss \"%s\" \"%s\"", config.path, delta, hint)
	setCall := configObject.Call(ConfigSetCall, 0, delta, hint)

	if setCall.Err != nil {
		return setCall.Err
	}
	var setResult bool
	if err := setCall.Store(&setResult); err != nil {
		return fmt.Errorf("failed to store dbus set reply: %w", err)
	}
	if !setResult {
		return fmt.Errorf("configration is not valid")
	}
	return nil
}

//nolint:unused
func (config *Config) try(timeout time.Duration) error {
	configObject := config.conn.Object(InterfacePath, dbus.ObjectPath(config.path))
	tryTimeout := uint32(timeout.Seconds())
	config.log.Debugf("trying configuration for %d seconds", tryTimeout)
	config.executionLog.Infof("busctl --system call io.netplan.Netplan %s io.netplan.Netplan.Config Try", config.path)
	call := configObject.Call(ConfigTryCall, 0, tryTimeout)
	if call.Err != nil {
		return call.Err
	}
	var tryResult bool
	if err := call.Store(&tryResult); err != nil {
		return fmt.Errorf("failed to store dbus try reply: %w", err)
	}
	if !tryResult {
		return fmt.Errorf("failed to try configuration within %d seconds", tryTimeout)
	}
	return nil
}

func (config *Config) apply() netplan.Error {
	config.log.Debugf("applying configuration")
	configObject := config.conn.Object(InterfacePath, dbus.ObjectPath(config.path))
	config.executionLog.Infof("busctl --system call io.netplan.Netplan %s io.netplan.Netplan.Config Apply", config.path)
	applyCall := configObject.Call(ConfigApplyCall, 0)
	if applyCall.Err != nil {
		config.log.Debugf("error applying configuration. err: %s", applyCall.Err.Error())
		return netplan.ParseError(applyCall.Err)
	}
	var applyResult bool
	if err := applyCall.Store(&applyResult); err != nil {
		return netplan.ParseError(err)
	}
	if !applyResult {
		return netplan.InvalidConfigurationError{}
	}
	return nil
}
func (config *Config) Set(state *netplan.State) netplan.Error {
	var errors []netplan.Error
	successfullTotalOperations := 0

	for {
		successfullPartialOperations := 0
		iterator := state.DeviceIterator()
		errors = make([]netplan.Error, 0)
		for iterator.HasNext() {
			current := iterator.Next()
			path := fmt.Sprintf("%s.%s", netplan.GetInterfaceTypeStatePath(current.Type), netplan.SanitizeDeviceName(current.Name))
			hint := config.hint
			if err := config.setProperty(hint, path, current.Device); err != nil {
				errors = append(errors, netplan.ParseError(err))
			} else {
				successfullPartialOperations++
			}
		}
		// If the number of succesfull operations did not increase in the last round, it means that
		//	1) we finished (i.e. errors should be empty) or
		//	2) there are interfaces that can't be set
		// Both of those outcomes should end the main loop
		if successfullPartialOperations <= successfullTotalOperations || len(errors) == 0 {
			break
		}
		successfullTotalOperations = successfullPartialOperations
	}
	if len(errors) > 0 {
		return netplan.MultipleErrors{Errors: errors}
	}
	if err := config.setProperty(config.hint, "network.version", state.Network.Version); err != nil {
		return netplan.InvalidConfigurationError{Err: err}
	}
	return nil
}

//nolint:unused
func (config *Config) canTry() (bool, error) {
	state, err := config.Get()
	if err != nil {
		return false, err
	}
	return !state.ContainsVirtualInterfaces(), nil
}

//nolint:revive
func (config *Config) Apply() netplan.Error {
	source := config.initialState
	if target, err := config.Get(); err != nil {
		return err
	} else {
		if virtualInterfacesToRemove, err := netplan.GetChangedVirtualInterfaces(source, target); err != nil {
			return netplan.ParseError(err)
		} else if len(virtualInterfacesToRemove) > 0 {
			config.log.Warnf("removing existing links for virtual interfaces before netplan apply")
			for _, link := range virtualInterfacesToRemove {
				if err := config.netManager.Delete(link); err != nil {
					config.log.Warnf("error deleting %s link %s. err: %s", link.Type, link.Name, err)
				}
			}
		}
	}
	config.log.Infof("applying new netplan configuration")
	if err := config.apply(); err != nil {
		return err
	}

	config.log.Infof("netplan apply was successful")
	return nil
}

func (config *Config) IsSynced() bool {
	newState, _ := config.Get()
	return config.initialState.Equals(newState)
}
func (*Client) Generate() netplan.Error {
	return nil
}

func (config *Config) Get() (*netplan.State, netplan.Error) {
	configObject := config.conn.Object(InterfacePath, dbus.ObjectPath(config.path))
	config.executionLog.Infof("busctl --system call io.netplan.Netplan %s io.netplan.Netplan.Config Get", config.path)
	call := configObject.Call(ConfigGetCall, 0)
	if call.Err != nil {
		return nil, netplan.ParseError(call.Err)
	}
	var rawState string
	if err := call.Store(&rawState); err != nil {
		return nil, netplan.ParseError(err)
	}
	var state netplan.State
	if err := yaml.Unmarshal([]byte(rawState), &state); err != nil {
		return nil, netplan.ParseError(err)
	}
	return &state, nil
}
