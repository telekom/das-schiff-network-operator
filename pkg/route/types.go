package route

import "github.com/vishvananda/netlink"

type Information struct {
	TableID       int
	VrfName       string
	RouteProtocol netlink.RouteProtocol
	AddressFamily string
	Quantity      int
}

type Key struct {
	TableID, RouteProtocol, AddressFamily int
}
