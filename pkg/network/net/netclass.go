package net

import (
	"fmt"
	"os"
	"path"

	"github.com/sirupsen/logrus"
)

const (
	bondingMasters = "bonding_masters"
)

type netClassManager struct {
	path string
	log  *logrus.Entry
}

func newNetClassManager(path string) netClassManager {
	return netClassManager{
		path: path,
		log:  logrus.WithField("name", "net-class-manager").WithField("root-dir", path),
	}
}
func (n *netClassManager) bondsIndex() string {
	return path.Join(n.path, bondingMasters)
}

func (n *netClassManager) Delete(i Interface) error {
	switch i.Type {
	case InterfaceTypeBond:
		n.log.Infof("removing bond %s", i.Name)
		data := fmt.Sprintf("-%s", i.Name)
		return os.WriteFile(n.bondsIndex(), []byte(data), 0777)
	case InterfaceTypeBridge:
		return fmt.Errorf("removing bridge is not supported using netclass")
	}

	return nil
}
