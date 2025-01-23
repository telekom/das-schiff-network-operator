package dbus

import "github.com/telekom/das-schiff-network-operator/pkg/network/net"

type Opts struct {
	SocketPath       string
	NetManager       net.Manager
	ExecutionLogPath string
}
