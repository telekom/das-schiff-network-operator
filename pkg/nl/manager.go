package nl

import (
	"net"

	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

const (
	vrfTableStart = 50
	vrfTableEnd   = 80

	bridgePrefix   = "br."
	vxlanPrefix    = "vx."
	layer2SVI      = "l2."
	vlanPrefix     = "vlan."
	loopbackPrefix = "lo."

	underlayInterfaceName = "dum.underlay"

	vxlanPort  = 4789
	defaultMtu = 9000
)

var macPrefix = []byte("\x02\x54")

type Manager struct {
	toolkit    ToolkitInterface
	baseConfig config.BaseConfig
}

func NewManager(toolkit ToolkitInterface, baseConfig config.BaseConfig) *Manager {
	return &Manager{toolkit: toolkit, baseConfig: baseConfig}
}

func (n *Manager) GetUnderlayIP() (net.IP, error) {
	_, ip, err := n.getUnderlayInterfaceAndIP()
	return ip, err
}
