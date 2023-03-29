package monitoring

import (
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	ctrl "sigs.k8s.io/controller-runtime"
)

type frrCollector struct {
	// routesDesc typedFactoryDesc
	frr    *frr.FRRCLI
	logger logr.Logger
}

func init() {
	registerCollector("frr", NewFRRCollector)
}

// NewNetlinkCollector returns a new Collector exposing buddyinfo stats.
func NewFRRCollector() (Collector, error) {
	frrCli, err := frr.NewFRRCLI()
	if err != nil {
		return nil, err
	}
	collector := frrCollector{
		// routesDesc: typedFactoryDesc{
		// 	desc: prometheus.NewDesc(
		// 		prometheus.BuildFQName(namespace, "frr", "routes"),
		// 		"The number of routes currently in the Linux Dataplane.",
		// 		[]string{"table", "protocol", "address_family"},
		// 		nil,
		// 	),
		// 	valueType: prometheus.GaugeValue,
		// },
		frr:    frrCli,
		logger: ctrl.Log.WithName("frr.collector"),
	}

	return &collector, nil
}

func (c *frrCollector) Update(ch chan<- prometheus.Metric) error {
	// routes, err := (nil, nil)
	// c.logger.Info("I am in the netlink collector")
	// if err != nil {
	// 	return err
	// }
	// for _, route := range routes {
	// 	ch <- c.routesDesc.mustNewConstMetric(float64(route.Quantity), fmt.Sprint(route.TableId), route.RouteProtocol.String(), route.AddressFamily)
	// }
	return nil
}
