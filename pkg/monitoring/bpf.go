package monitoring

import (
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	ctrl "sigs.k8s.io/controller-runtime"
)

type bpfCollector struct {
	// routesDesc typedFactoryDesc
	bpf    string
	logger logr.Logger
}

func init() {
	registerCollector("bpf", NewBPFCollector)
}

func NewBPFCollector() (Collector, error) {
	// frrCli, err := bpf.
	var err error
	if err != nil {
		return nil, err
	}
	collector := bpfCollector{
		// routesDesc: typedFactoryDesc{
		// 	desc: prometheus.NewDesc(
		// 		prometheus.BuildFQName(namespace, "frr", "routes"),
		// 		"The number of routes currently in the Linux Dataplane.",
		// 		[]string{"table", "protocol", "address_family"},
		// 		nil,
		// 	),
		// 	valueType: prometheus.GaugeValue,
		// },
		// bpf:    nil,
		logger: ctrl.Log.WithName("bpf.collector"),
	}

	return &collector, nil
}

func (c *bpfCollector) Update(ch chan<- prometheus.Metric) error {
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
