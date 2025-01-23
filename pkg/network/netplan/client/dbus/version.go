package dbus

import "github.com/telekom/das-schiff-network-operator/pkg/network/netplan"

const (
	VersionProperty = "org.freedesktop.NetworkManager.Version"
)

func (client *Client) Version() (string, netplan.Error) {
	return "", nil
}
