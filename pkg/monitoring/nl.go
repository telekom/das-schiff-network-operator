package monitoring

import (
	"fmt"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	ctrl "sigs.k8s.io/controller-runtime"
)

var ()

type netlinkCollector struct {
	routesDesc    typedFactoryDesc
	neighborsDesc typedFactoryDesc
	netlink       *nl.NetlinkManager
	logger        logr.Logger
}

func init() {
	registerCollector("netlink", NewNetlinkCollector)
}

// NewNetlinkCollector returns a new Collector exposing buddyinfo stats.
func NewNetlinkCollector() (Collector, error) {
	collector := netlinkCollector{
		routesDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "netlink", "routes"),
				"The number of routes currently in the Linux Dataplane.",
				[]string{"table", "vrf_name", "protocol", "address_family"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		neighborsDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "netlink", "neighbors"),
				"The number of neighbors currently in the Linux Dataplane.",
				[]string{"mac", "ip", "interface_name", "address_family", "status"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		netlink: &nl.NetlinkManager{},
		logger:  ctrl.Log.WithName("netlink.collector"),
	}

	return &collector, nil
}

func (c *netlinkCollector) Update(ch chan<- prometheus.Metric) error {
	routes, err := c.netlink.ListRoutes()
	if err != nil {
		return err
	}
	neighbors, err := c.netlink.ListNeighbors()
	c.logger.Info("I am in the netlink collector")
	if err != nil {
		return err
	}
	for _, neighbor := range neighbors {
		ch <- c.neighborsDesc.mustNewConstMetric(1.0, neighbor.MAC, neighbor.IP, neighbor.Interface, neighbor.Family, neighbor.State)
	}
	for _, route := range routes {
		ch <- c.routesDesc.mustNewConstMetric(float64(route.Quantity), fmt.Sprint(route.TableId), route.VrfName, route.RouteProtocol.String(), route.AddressFamily)
	}
	return nil
}
