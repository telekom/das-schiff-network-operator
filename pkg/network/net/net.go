package net

type InterfaceType int

const (
	InterfaceTypeEthernet InterfaceType = iota
	InterfaceTypeModem
	InterfaceTypeWifi
	InterfaceTypeBridge
	InterfaceTypeBond
	InterfaceTypeTunnel
	InterfaceTypeVLan
	InterfaceTypeVRF
	InterfaceTypeDummy
)

func (i InterfaceType) String() string {
	switch i {
	case InterfaceTypeEthernet:
		return "ethernet"
	case InterfaceTypeModem:
		return "modem"
	case InterfaceTypeWifi:
		return "wifi"
	case InterfaceTypeBridge:
		return "bridge"
	case InterfaceTypeBond:
		return "bond"
	case InterfaceTypeTunnel:
		return "tunnel"
	case InterfaceTypeVLan:
		return "vlan"
	case InterfaceTypeVRF:
		return "vrf"
	case InterfaceTypeDummy:
		return "dummy"
	}
	return ""
}

type Interface struct {
	Type InterfaceType
	Name string
}
type Manager interface {
	Delete(Interface) error
}
type Opts struct {
	NetClassPath string
}

func NewManager(opts Opts) Manager {
	if opts.NetClassPath != "" {
		netClassManager := newNetClassManager(opts.NetClassPath)
		return &netClassManager
	}
	netLinkManager := newNetLinkManager()
	return &netLinkManager
}
