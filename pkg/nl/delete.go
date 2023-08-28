package nl

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

func (*NetlinkManager) deleteLink(name string) error {
	if err := netlink.LinkDel(&netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
		return fmt.Errorf("error while deleting link: %w", err)
	}
	return nil
}
