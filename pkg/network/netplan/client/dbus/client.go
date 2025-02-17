package dbus

import (
	"fmt"

	"github.com/godbus/dbus/v5"
	"github.com/sirupsen/logrus"
	"github.com/telekom/das-schiff-network-operator/pkg/network/net"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	InterfacePath = "io.netplan.Netplan"
	ObjectPath    = "/io/netplan/Netplan"

	ConfigCall = "io.netplan.Netplan.Config"
	GetCall    = "io.netplan.Netplan.Get"
	ApplyCall  = "io.netplan.Netplan.Apply"
	InfoCall   = "io.netplan.Netplan.Info"
)

type Client struct {
	hint         string
	initialHints []string
	conn         *dbus.Conn
	netManager   net.Manager
	logger       *logrus.Logger
}

func New(hint string, initialHints []string, opts Opts) (*Client, error) {
	logrus.Infof("dialing with dbus using address %s", opts.SocketPath)
	dbusConn, err := dbus.Dial(opts.SocketPath)
	if err != nil {
		logrus.Errorf("error dialing connection with dbus using %s. err: %s", opts.SocketPath, err)
		return nil, fmt.Errorf("error dialing connection with dbus using %s. err: %w", opts.SocketPath, err)
	}
	if err := dbusConn.Auth(nil); err != nil {
		logrus.Errorf("error authenticating with dbus. err: %s", err)
		dbusConn.Close()
		return nil, fmt.Errorf("error authenticating with dbus. err: %w", err)
	}

	if err := dbusConn.Hello(); err != nil {
		logrus.Errorf("error sending Hello message to dbus. err: %s", err)
		dbusConn.Close()
		return nil, fmt.Errorf("error sending Hello message to dbus. err: %w", err)
	}
	executionLog := logrus.New()
	executionLog.SetFormatter(&logrus.TextFormatter{DisableColors: true, DisableQuote: true})
	executionLog.SetLevel(logrus.FatalLevel)
	client := Client{
		hint:         hint,
		initialHints: initialHints,
		conn:         dbusConn,
		netManager:   opts.NetManager,
		logger:       executionLog,
	}

	if opts.ExecutionLogPath != "" {
		lumberjackLogger := &lumberjack.Logger{
			// Log file abbsolute path, os agnostic
			Filename:   opts.ExecutionLogPath,
			MaxSize:    1,    // MB //nolint:mnd
			MaxBackups: 10,   //nolint:mnd
			MaxAge:     7,    //nolint:mnd // days
			Compress:   true, // disabled by default
		}
		client.logger.SetOutput(lumberjackLogger)
		client.logger.SetLevel(logrus.InfoLevel)
	}
	return &client, nil
}

func (client *Client) Close() {
	client.conn.Close()
	client.conn = nil
}
func (client *Client) config() (*Config, netplan.Error) {
	dbusConfig, err := newConfig(client.hint, client.initialHints, client.conn, client.netManager, client.logger)
	return &dbusConfig, netplan.ParseError(err)
}
func (client *Client) Initialize() (config.Config, netplan.Error) {
	cfg, err := client.config()
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}
func (client *Client) Get() (netplan.State, netplan.Error) {
	tempConfig, err := client.config()
	if err != nil {
		return netplan.State{}, err
	}
	if err := tempConfig.Discard(); err != nil {
		return netplan.State{}, err
	}
	return tempConfig.initialState, nil
}

type featuresVariantType [][]interface{}

func (fvt featuresVariantType) getFeatures() ([]string, error) {
	resultVariant, ok := fvt[0][1].(dbus.Variant)
	if !ok {
		return nil, fmt.Errorf("failed to cast feature %v to dbus Variant", fvt[0][1])
	}
	var result []string
	if err := resultVariant.Store(&result); err != nil {
		return nil, fmt.Errorf("failed to store variant in the result: %w", err)
	}
	return result, nil
}

func (client *Client) Info() ([]string, netplan.Error) {
	var result dbus.Variant
	object := client.conn.Object(InterfacePath, ObjectPath)
	client.logger.Infof("busctl --system call io.netplan.Netplan /io/netplan/Netplan io.netplan.Netplan Info")
	call := object.Call(InfoCall, 0)
	if call.Err != nil {
		return nil, netplan.ParseError(call.Err)
	}
	if err := call.Store(&result); err != nil {
		return nil, netplan.ParseError(err)
	}
	var featuresVariant featuresVariantType
	if err := result.Store(&featuresVariant); err != nil {
		return nil, netplan.ParseError(err)
	}
	fv, err := featuresVariant.getFeatures()
	if err != nil {
		return nil, netplan.ParseError(err)
	}
	return fv, nil
}
