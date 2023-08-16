package frr

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"text/template"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

var (
	FRR_UNIT        = "frr.service"
	FRR_PERMISSIONS = fs.FileMode(0640)
)

type FRRManager struct {
	configTemplate *template.Template
	ConfigPath     string
	TemplatePath   string
}

type PrefixList struct {
	Items     []PrefixedRouteItem
	Seq       int
	Community *int
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

type FRRConfiguration struct {
	ASN  int
	VRFs []VRFConfiguration
}

func NewFRRManager() *FRRManager {
	return &FRRManager{
		ConfigPath:   "/etc/frr/frr.conf",
		TemplatePath: "/etc/frr/frr.conf.tpl",
	}
}

func (m *FRRManager) Init() error {
	if _, err := os.Stat(m.TemplatePath); errors.Is(err, os.ErrNotExist) {
		err = generateTemplateConfig(m.TemplatePath, m.ConfigPath)
		if err != nil {
			return err
		}
	}

	bytes, err := os.ReadFile(m.TemplatePath)
	if err != nil {
		return err
	}
	tpl, err := template.New("frr_config").Parse(string(bytes))
	if err != nil {
		return err
	}
	m.configTemplate = tpl
	return nil
}

func (m *FRRManager) ReloadFRR() error {
	con, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return err
	}
	defer con.Close()

	_, err = con.ReloadUnitContext(context.Background(), FRR_UNIT, "fail", nil)
	return err
}

func (v VRFConfiguration) ShouldTemplateVRF() bool {
	return v.VNI != config.SkipVrfTemplateVni
}

func (v VRFConfiguration) ShouldDefineRT() bool {
	return v.RT != ""
}
