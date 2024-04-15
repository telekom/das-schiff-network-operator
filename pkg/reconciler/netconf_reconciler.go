package reconciler

import (
	"context"
	"errors"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

type NetconfReconciler struct{}

func NewNetconfReconciler() (Adapter, error) {
	return &NetconfReconciler{}, nil
}

func (*NetconfReconciler) checkHealth(context.Context) error {
	return errors.ErrUnsupported
}

func (*NetconfReconciler) getConfig() *config.Config {
	return nil
}

func (*NetconfReconciler) reconcileLayer3([]networkv1alpha1.VRFRouteConfiguration, []networkv1alpha1.RoutingTable) error {
	return errors.ErrUnsupported
}

func (*NetconfReconciler) reconcileLayer2([]networkv1alpha1.Layer2NetworkConfiguration) error {
	return errors.ErrUnsupported
}
