package adapters

import (
	"context"
	"errors"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/worker"
)

type netNS struct{}

func New() (worker.Adapter, error) {
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

func NewClient() (worker.Client, error) {
	return &netNSClient{}, errors.ErrUnsupported
}

func (*netNSClient) SendConfig(context.Context, *v1alpha1.NodeNetworkConfig) error {
	return errors.ErrUnsupported
}
