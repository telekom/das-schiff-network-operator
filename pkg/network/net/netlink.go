package net

import (
	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

type netLinkManager struct{}

func newNetLinkManager() netLinkManager {
	return netLinkManager{}
}
func (n *netLinkManager) get(name string) (netlink.Link, error) {
	logrus.Debugf("searching network link %s", name)
	return netlink.LinkByName(name)
}
func (n *netLinkManager) Delete(i Interface) error {
	switch i.Type {
	case InterfaceTypeBond:
		if link, err := n.get(i.Name); err != nil {
			return err
		} else {
			logrus.Infof("deleting link %s", link.Attrs().Name)
			return netlink.LinkDel(link)
		}
	}
	return nil
}
