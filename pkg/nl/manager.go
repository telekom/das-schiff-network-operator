package nl

import (
	"net"
)

const (
	vrfTableStart = 50
	vrfTableEnd   = 80

	vrfPrefix          = "vr."
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
	toolkit ToolkitInterface
}

func NewManager(toolkit ToolkitInterface) *Manager {
	return &Manager{toolkit: toolkit}
}

func (n *Manager) GetUnderlayIP() (net.IP, error) {
	_, ip, err := n.getUnderlayInterfaceAndIP()
	return ip, err
}
