package frr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"text/template"

	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/frr/dbus"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

const defaultPermissions = 0o640

var (
	frrUnit        = "frr.service"
	frrPermissions = fs.FileMode(defaultPermissions)
)

//go:generate mockgen -destination ./mock/mock_frr.go . ManagerInterface
type ManagerInterface interface {
	Init(mgmtVrf string) error
	ReloadFRR() error
	RestartFRR() error
	GetStatusFRR() (activeState, subState string, err error)
	Configure(in Configuration, nm *nl.Manager) (bool, error)
	SetConfigPath(path string)
}

type Manager struct {
	configTemplate *template.Template

	ipv4MgmtRouteMapIn *string
	ipv6MgmtRouteMapIn *string
	mgmtVrf            string

	ConfigPath   string
	TemplatePath string
	Cli          *Cli
	dbusToolkit  dbus.System
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
	MTU           int
	RT            string
	IsTaaS        bool
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
		dbusToolkit:  &dbus.Toolkit{},
	}
}

func (m *Manager) Init(mgmtVrf string) error {
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

	m.mgmtVrf = mgmtVrf
	routeMap, err := getRouteMapName(m.ConfigPath, "ipv4", m.mgmtVrf)
	if err != nil {
		return fmt.Errorf("error getting v4 mgmt route-map from FRR config: %w", err)
	}
	m.ipv4MgmtRouteMapIn = routeMap

	routeMap, err = getRouteMapName(m.ConfigPath, "ipv6", m.mgmtVrf)
	if err != nil {
		return fmt.Errorf("error getting v6 mgmt route-map from FRR config: %w", err)
	}
	m.ipv6MgmtRouteMapIn = routeMap

	return nil
}

func (m *Manager) ReloadFRR() error {
	con, err := m.dbusToolkit.NewConn(context.Background())
	if err != nil {
		return fmt.Errorf("error creating new D-Bus connection: %w", err)
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

func (m *Manager) RestartFRR() error {
	con, err := m.dbusToolkit.NewConn(context.Background())
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

func (m *Manager) GetStatusFRR() (activeState, subState string, err error) {
	con, err := m.dbusToolkit.NewConn(context.Background())
	if err != nil {
		return "", "", fmt.Errorf("error creating D-Bus connection: %w", err)
	}
	defer con.Close()

	prop, err := con.GetUnitPropertiesContext(context.Background(), frrUnit)
	if err != nil {
		return "", "", fmt.Errorf("error getting unit %s properties: %w", frrUnit, err)
	}
	var ok bool
	activeState, ok = prop["ActiveState"].(string)
	if !ok {
		return "", "", fmt.Errorf("error casting property %v [\"ActiveState\"] as string", prop)
	}
	subState, ok = prop["SubState"].(string)
	if !ok {
		return activeState, "", fmt.Errorf("error casting property %v [\"SubState\"] as string", prop)
	}
	return activeState, subState, nil
}

func (m *Manager) SetConfigPath(path string) {
	m.ConfigPath = path
}

func (v *VRFConfiguration) ShouldTemplateVRF() bool {
	return v.VNI != config.SkipVrfTemplateVni
}

func (v *VRFConfiguration) ShouldDefineRT() bool {
	return v.RT != ""
}
