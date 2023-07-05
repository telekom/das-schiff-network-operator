package monitoring

import (
	"fmt"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	ctrl "sigs.k8s.io/controller-runtime"
)

type frrCollector struct {
	routesDesc typedFactoryDesc
	frr        *frr.FRRManager
	logger     logr.Logger
}

func init() {
	registerCollector("frr", NewFRRCollector)
}

// NewNetlinkCollector returns a new Collector exposing buddyinfo stats.
func NewFRRCollector() (Collector, error) {
	collector := frrCollector{
		routesDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "routes"),
				"The number of routes currently in the frr Controlplane.",
				[]string{"table", "protocol", "address_family"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		frr:    frr.NewFRRManager(),
		logger: ctrl.Log.WithName("frr.collector"),
	}

	return &collector, nil
}

func (c *frrCollector) Update(ch chan<- prometheus.Metric) error {
	routes, err := c.frr.ListRoutes("")
	c.logger.Info("I am in the frr collector")
	if err != nil {
		return err
	}
	for _, routePath := range routes {
		ch <- c.routesDesc.mustNewConstMetric(float64(routePath.Quantity), fmt.Sprint(routePath.TableId), routePath.RouteProtocol.String(), routePath.AddressFamily)
	}
	return nil
}
