package frr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"text/template"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

const defaultPermissions = 0o640

var (
	frrUnit        = "frr.service"
	frrPermissions = fs.FileMode(defaultPermissions)
)

type Manager struct {
	configTemplate *template.Template
	ConfigPath     string
	TemplatePath   string
	Cli            *Cli
}

type PrefixList struct {
	Items     []PrefixedRouteItem
	Seq       int
	Community *string
}

type PrefixedRouteItem struct {
	CIDR   net.IPNet
	IPv6   bool
	Seq    int
	Action string
	GE     *int
	LE     *int
}

type VRFConfiguration struct {
	Name          string
	VNI           int
	RT            string
	AggregateIPv4 []string
	AggregateIPv6 []string
	Import        []PrefixList
	Export        []PrefixList
}

type Configuration struct {
	ASN  int
	VRFs []VRFConfiguration
}

func NewFRRManager() *Manager {
	return &Manager{
		ConfigPath:   "/etc/frr/frr.conf",
		TemplatePath: "/etc/frr/frr.conf.tpl",
		Cli:          NewCli(),
	}
}

func (m *Manager) Init() error {
	if _, err := os.Stat(m.TemplatePath); errors.Is(err, os.ErrNotExist) {
		err = generateTemplateConfig(m.TemplatePath, m.ConfigPath)
		if err != nil {
			return err
		}
	}

	bytes, err := os.ReadFile(m.TemplatePath)
	if err != nil {
		return fmt.Errorf("error reading template file %s: %w", m.TemplatePath, err)
	}
	tpl, err := template.New("frr_config").Parse(string(bytes))
	if err != nil {
		return fmt.Errorf("error creating new FRR config: %w", err)
	}
	m.configTemplate = tpl
	return nil
}

func (*Manager) ReloadFRR() error {
	con, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return fmt.Errorf("error creating nee D-Bus connection: %w", err)
	}
	defer con.Close()

	jobChan := make(chan string)
	if _, err = con.ReloadUnitContext(context.Background(), frrUnit, "fail", jobChan); err != nil {
		return fmt.Errorf("error reloading %s context: %w", frrUnit, err)
	}
	reloadStatus := <-jobChan
	if reloadStatus != "done" {
		return fmt.Errorf("error reloading %s, job status is %s", frrUnit, reloadStatus)
	}
	return nil
}

func (*Manager) RestartFRR() error {
	con, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return fmt.Errorf("error creating nee D-Bus connection: %w", err)
	}
	defer con.Close()

	jobChan := make(chan string)
	if _, err = con.RestartUnitContext(context.Background(), frrUnit, "fail", jobChan); err != nil {
		return fmt.Errorf("error restarting %s context: %w", frrUnit, err)
	}
	restartStatus := <-jobChan
	if restartStatus != "done" {
		return fmt.Errorf("error restarting %s, job status is %s", restartStatus, restartStatus)
	}
	return nil
}

func (*Manager) GetStatusFRR() (activeState, subState string, err error) {
	con, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return "", "", fmt.Errorf("error creating D-Bus connection: %w", err)
	}
	defer con.Close()

	prop, err := con.GetUnitPropertiesContext(context.Background(), frrUnit)
	if err != nil {
		return "", "", fmt.Errorf("error getting unit %s properties: %w", frrUnit, err)
	}

	return prop["ActiveState"].(string), prop["SubState"].(string), nil
}

func (v *VRFConfiguration) ShouldTemplateVRF() bool {
	return v.VNI != config.SkipVrfTemplateVni
}

func (v *VRFConfiguration) ShouldDefineRT() bool {
	return v.RT != ""
}
