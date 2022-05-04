package nl

import "github.com/vishvananda/netlink"

func (n *NetlinkManager) deleteLink(name string) error {
	return netlink.LinkDel(&netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Name: name}})
}
