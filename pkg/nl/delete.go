package nl

import (
	"errors"
	"fmt"
	"syscall"

	"github.com/vishvananda/netlink"
)

func (n *Manager) deleteLink(name string) error {
	if err := n.toolkit.LinkDel(&netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
		// Treat ENODEV and EINVAL as success — the link is already gone.
		if errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.EINVAL) {
			return nil
		}
		return fmt.Errorf("error while deleting link: %w", err)
	}
	return nil
}
