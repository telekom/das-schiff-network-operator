//nolint:wrapcheck
package nltoolkit

import (
	"net"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
)

//go:generate mockgen -destination ./mock/mock_nl.go . ToolkitInterface
type ToolkitInterface interface {
	LinkByIndex(index int) (netlink.Link, error)
	LinkByName(name string) (netlink.Link, error)
	LinkList() ([]netlink.Link, error)
	NeighList(linkIndex int, family int) ([]netlink.Neigh, error)
	NeighSubscribeWithOptions(ch chan<- netlink.NeighUpdate, done <-chan struct{}, options netlink.NeighSubscribeOptions) error
	NeighSet(neigh *netlink.Neigh) error
	NewIPNet(ip net.IP) *net.IPNet
	RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error)
	RouteDel(route *netlink.Route) error
	RouteAdd(route *netlink.Route) error
	AddrList(link netlink.Link, family int) ([]netlink.Addr, error)
	VethPeerIndex(link *netlink.Veth) (int, error)
	ParseAddr(s string) (*netlink.Addr, error)
	LinkDel(link netlink.Link) error
	LinkSetUp(link netlink.Link) error
	LinkAdd(link netlink.Link) error
	AddrAdd(link netlink.Link, addr *netlink.Addr) error
	AddrDel(link netlink.Link, addr *netlink.Addr) error
	AddrReplace(link netlink.Link, addr *netlink.Addr) error
	LinkSetLearning(link netlink.Link, mode bool) error
	LinkSetHairpin(link netlink.Link, mode bool) error
	ExecuteNetlinkRequest(req *nl.NetlinkRequest, sockType int, resType uint16) ([][]byte, error)
	LinkSetMTU(link netlink.Link, mtu int) error
	LinkSetDown(link netlink.Link) error
	LinkSetHardwareAddr(link netlink.Link, hwaddr net.HardwareAddr) error
	LinkSetMasterByIndex(link netlink.Link, masterIndex int) error
	LinkSetNoMaster(link netlink.Link) error
	LinkGetProtinfo(link netlink.Link) (netlink.Protinfo, error)
	LinkSetMaster(link netlink.Link, master netlink.Link) error
}

type Toolkit struct{}

func (*Toolkit) LinkByIndex(index int) (netlink.Link, error) {
	return netlink.LinkByIndex(index)
}

func (*Toolkit) LinkByName(name string) (netlink.Link, error) {
	return netlink.LinkByName(name)
}

func (*Toolkit) LinkList() ([]netlink.Link, error) {
	return netlink.LinkList()
}

func (*Toolkit) NeighList(linkIndex, family int) ([]netlink.Neigh, error) {
	return netlink.NeighList(linkIndex, family)
}

func (*Toolkit) NeighSubscribeWithOptions(ch chan<- netlink.NeighUpdate, done <-chan struct{}, options netlink.NeighSubscribeOptions) error {
	return netlink.NeighSubscribeWithOptions(ch, done, options)
}

func (*Toolkit) NeighSet(neigh *netlink.Neigh) error {
	return netlink.NeighSet(neigh)
}

func (*Toolkit) NewIPNet(ip net.IP) *net.IPNet {
	return netlink.NewIPNet(ip)
}

func (*Toolkit) RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error) {
	return netlink.RouteListFiltered(family, filter, filterMask)
}

func (*Toolkit) RouteDel(route *netlink.Route) error {
	return netlink.RouteDel(route)
}

func (*Toolkit) RouteAdd(route *netlink.Route) error {
	return netlink.RouteAdd(route)
}

func (*Toolkit) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	return netlink.AddrList(link, family)
}

func (*Toolkit) VethPeerIndex(link *netlink.Veth) (int, error) {
	return netlink.VethPeerIndex(link)
}

func (*Toolkit) ParseAddr(s string) (*netlink.Addr, error) {
	return netlink.ParseAddr(s)
}

func (*Toolkit) LinkDel(link netlink.Link) error {
	return netlink.LinkDel(link)
}

func (*Toolkit) LinkSetUp(link netlink.Link) error {
	return netlink.LinkSetUp(link)
}

func (*Toolkit) LinkAdd(link netlink.Link) error {
	return netlink.LinkAdd(link)
}

func (*Toolkit) AddrAdd(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrAdd(link, addr)
}

func (*Toolkit) AddrReplace(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrReplace(link, addr)
}

func (*Toolkit) LinkSetLearning(link netlink.Link, mode bool) error {
	return netlink.LinkSetLearning(link, mode)
}

func (*Toolkit) LinkSetHairpin(link netlink.Link, mode bool) error {
	return netlink.LinkSetHairpin(link, mode)
}

func (*Toolkit) ExecuteNetlinkRequest(req *nl.NetlinkRequest, sockType int, resType uint16) ([][]byte, error) {
	return req.Execute(sockType, resType)
}

func (*Toolkit) LinkSetMTU(link netlink.Link, mtu int) error {
	return netlink.LinkSetMTU(link, mtu)
}

func (*Toolkit) LinkSetDown(link netlink.Link) error {
	return netlink.LinkSetDown(link)
}

func (*Toolkit) LinkSetHardwareAddr(link netlink.Link, hwaddr net.HardwareAddr) error {
	return netlink.LinkSetHardwareAddr(link, hwaddr)
}

func (*Toolkit) LinkSetMasterByIndex(link netlink.Link, masterIndex int) error {
	return netlink.LinkSetMasterByIndex(link, masterIndex)
}

func (*Toolkit) LinkSetNoMaster(link netlink.Link) error {
	return netlink.LinkSetNoMaster(link)
}

func (*Toolkit) LinkGetProtinfo(link netlink.Link) (netlink.Protinfo, error) {
	return netlink.LinkGetProtinfo(link)
}

func (*Toolkit) LinkSetMaster(link, master netlink.Link) error {
	return netlink.LinkSetMaster(link, master)
}

func (*Toolkit) AddrDel(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrDel(link, addr)
}
