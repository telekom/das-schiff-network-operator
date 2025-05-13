package net

import (
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

type netLinkManager struct{}

func newNetLinkManager() netLinkManager {
	return netLinkManager{}
}
func (*netLinkManager) get(name string) (netlink.Link, error) {
	logrus.Debugf("searching network link %s", name)
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get link '%s': %w", name, err)
	}
	return link, nil
}
func (n *netLinkManager) Delete(i Interface) error {
	if i.Type == InterfaceTypeBond {
		var link netlink.Link
		var err error
		if link, err = n.get(i.Name); err != nil {
			return fmt.Errorf("failed to get interface %s: %w", i.Name, err)
		}
		logrus.Infof("deleting link %s", link.Attrs().Name)
		if err := netlink.LinkDel(link); err != nil {
			return fmt.Errorf("failed to delete link: %w", err)
		}
	}
	return nil
}
