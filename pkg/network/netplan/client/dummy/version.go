package dummy

import "github.com/telekom/das-schiff-network-operator/pkg/network/netplan"

func (*Client) Version() (string, netplan.Error) {
	return "", nil
}
