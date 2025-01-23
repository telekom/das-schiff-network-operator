package dummy

import (
	"os"

	"github.com/sirupsen/logrus"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan/config"
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
	directory    string
	initialHints []string
	logger       *logrus.Logger
	tempDir      bool
}

func New(hint string, initialHints []string, opts Opts) (*Client, error) {
	client := Client{
		hint:         hint,
		directory:    opts.Directory,
		initialHints: initialHints,
	}
	if opts.Directory == "" {
		dir, err := os.MkdirTemp("", "caas-network-operator")
		if err != nil {
			return nil, err
		}
		client.directory = dir
		client.tempDir = true
	}
	logrus.Infof("creating dummy netplan client using directory %s", client.directory)
	return &client, nil
}
func (client *Client) config() (*DummyConfig, netplan.Error) {
	dummyConfig, err := newConfig(client.hint, client.initialHints, client.directory)
	return &dummyConfig, netplan.ParseError(err)
}
func (client *Client) Close() {
	if client.tempDir {
		logrus.Infof("clearing temp dir used by dummy netplan client: %s", client.directory)
		os.RemoveAll(client.directory)
	}
}
func (client *Client) Initialize() (config.Config, netplan.Error) {
	if config, err := client.config(); err != nil {
		return config, err
	} else {
		return config, nil
	}
}
func (client *Client) Get() (netplan.State, netplan.Error) {
	if tempConfig, err := client.config(); err != nil {
		return netplan.State{}, err
	} else {
		if err := tempConfig.Discard(); err != nil {
			return netplan.State{}, err
		}
		return tempConfig.initialState, nil
	}
}

type featuresVariantType [][]interface{}

func (client *Client) Info() ([]string, netplan.Error) {
	return nil, nil
}
