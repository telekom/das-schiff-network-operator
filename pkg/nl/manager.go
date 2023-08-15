package nl

import (
	"net"
)

var (
	VRF_TABLE_START = 50
	VRF_TABLE_END   = 80

	VRF_PREFIX            = "vr."
	BRIDGE_PREFIX         = "br."
	VXLAN_PREFIX          = "vx."
	VRF_TO_DEFAULT_PREFIX = "vd."
	DEFAULT_TO_VRF_PREFIX = "dv."
	LAYER2_PREFIX         = "l2."
	MACVLAN_PREFIX        = "vlan."
	VETH_L2_PREFIX        = "l2v."

	UNDERLAY_LOOPBACK = "dum.underlay"

	VXLAN_PORT  = 4789
	DEFAULT_MTU = 9000

	MAC_PREFIX = []byte("\x02\x54")
)

type NetlinkManager struct {
}

func (n *NetlinkManager) GetUnderlayIP() (net.IP, error) {
	_, ip, err := getInterfaceAndIP(UNDERLAY_LOOPBACK)
	return ip, err
}
