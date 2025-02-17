package client

import (
	"fmt"
	"time"

	dbusv5 "github.com/godbus/dbus/v5"
	"github.com/sirupsen/logrus"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan/client/dbus"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan/client/direct"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan/client/dummy"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan/config"
)

type Client interface {
	Version() (string, netplan.Error)
	// Apply(hint string, state netplan.State, timeout time.Duration, persistFn func() error) netplan.Error
	Get() (netplan.State, netplan.Error)
	// Generate() netplan.Error
	Info() ([]string, netplan.Error)
	Initialize() (config.Config, netplan.Error)
}

const (
	defaultGwProbeTimeout = 120 * time.Second
	apiServerProbeTimeout = 120 * time.Second
	// DesiredStateConfigurationTimeout doubles the default gw ping probe and API server
	// connectivity check timeout to ensure the Checkpoint is alive before rolling it back.
	DesiredStateConfigurationTimeout = (defaultGwProbeTimeout + apiServerProbeTimeout) * 2
)

type Mode int

const (
	ClientModeDirect Mode = iota
	ClientModeDBus
	ClientModeDummy
)

type Opts struct {
	InitialHints     []string
	DummyOpts        dummy.Opts
	DbusOpts         dbus.Opts
	DirectClientOpts direct.Opts
}

func New(hint string, mode Mode, opts *Opts) (Client, error) {
	switch mode {
	case ClientModeDBus:
		logrus.Infof("dialing with dbus using address %s", opts.DbusOpts.SocketPath)
		dbusConn, err := dbusv5.Dial(opts.DbusOpts.SocketPath)
		if err != nil {
			logrus.Errorf("error dialing connection with dbus using %s. err: %s", opts.DbusOpts.SocketPath, err)
			return nil, fmt.Errorf("error dialing connection with dbus using %s. err: %w", opts.DbusOpts.SocketPath, err)
		}
		client, err := dbus.New(hint, opts.InitialHints, opts.DbusOpts, dbusConn)
		if err != nil {
			return nil, fmt.Errorf("failed to create new dbus client: %w", err)
		}
		return client, nil
	// case ClientModeDirect:
	// return direct.New(opts.DirectClientOpts), nil
	case ClientModeDummy:
		client, err := dummy.New(hint, opts.InitialHints, opts.DummyOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to create new dummy client: %w", err)
		}
		return client, nil
	default:
		return nil, nil
	}
}
