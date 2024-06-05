package adapters

import (
	"context"
	"errors"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/agent"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

type netNS struct{}

func New() (agent.Adapter, error) {
	return &netNS{}, nil
}

func (*netNS) CheckHealth() error {
	return errors.ErrUnsupported
}

func (*netNS) GetConfig() *config.Config {
	return nil
}

func (*netNS) ReconcileLayer3([]v1alpha1.VRFRouteConfigurationSpec, []v1alpha1.RoutingTableSpec) error {
	return errors.ErrUnsupported
}

func (*netNS) ReconcileLayer2([]v1alpha1.Layer2NetworkConfigurationSpec) error {
	return errors.ErrUnsupported
}

type netNSClient struct{}

func NewClient() agent.Client {
	return &netNSClient{}
}

func (*netNSClient) SendConfig(context.Context, *v1alpha1.NodeConfig) error {
	return errors.ErrUnsupported
}
