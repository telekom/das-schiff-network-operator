package nl

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

func (n *Manager) deleteLink(name string) error {
	if err := n.toolkit.LinkDel(&netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
		return fmt.Errorf("error while deleting link: %w", err)
	}
	return nil
}
