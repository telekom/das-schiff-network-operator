package route

import "github.com/vishvananda/netlink"

type RouteInformation struct {
	TableId       int
	VrfName       string
	RouteProtocol netlink.RouteProtocol
	AddressFamily string
	Quantity      int
}

type RouteKey struct {
	TableId, RouteProtocol, AddressFamily int
}
