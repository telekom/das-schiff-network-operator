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

	// linkTypeBridge is the netlink link type string for a bridge interface.
	linkTypeBridge = "bridge"
	// linkTypeVXLAN is the netlink link type string for a VXLAN interface.
	linkTypeVXLAN = "vxlan"

	underlayInterfaceName = "dum.underlay"

	vxlanPort  = 4789
	DefaultMtu = 9000
)

var macPrefix = []byte("\x02\x54")

type Manager struct {
	toolkit    ToolkitInterface
	baseConfig *config.BaseConfig
}

func NewManager(toolkit ToolkitInterface, baseConfig *config.BaseConfig) *Manager {
	return &Manager{toolkit: toolkit, baseConfig: baseConfig}
}

func (n *Manager) GetUnderlayIP() (net.IP, error) {
	_, ip, err := n.getUnderlayInterfaceAndIP()
	return ip, err
}
