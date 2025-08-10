package nl

import (
	"net"

	"github.com/telekom/das-schiff-network-operator/pkg/nltoolkit"
)

const (
	VrfPrefix = "vr."

	vrfTableStart = 50
	vrfTableEnd   = 80

	bridgePrefix       = "br."
	vxlanPrefix        = "vx."
	vrfToDefaultPrefix = "vd."
	defaultToVrfPrefix = "dv."
	layer2Prefix       = "l2."
	macvlanPrefix      = "vlan."
	vethL2Prefix       = "l2v."
	taasVrfPrefix      = "taas."

	underlayLoopback = "dum.underlay"

	vxlanPort  = 4789
	defaultMtu = 9000
)

var macPrefix = []byte("\x02\x54")

type Manager struct {
	toolkit nltoolkit.ToolkitInterface
}

func NewManager(toolkit nltoolkit.ToolkitInterface) *Manager {
	return &Manager{toolkit: toolkit}
}

func (n *Manager) GetUnderlayIP() (net.IP, error) {
	_, ip, err := n.getUnderlayInterfaceAndIP()
	return ip, err
}
