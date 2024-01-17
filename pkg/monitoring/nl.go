package monitoring

import (
	"fmt"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	ctrl "sigs.k8s.io/controller-runtime"
)

type netlinkCollector struct {
	routesFibDesc typedFactoryDesc
	neighborsDesc typedFactoryDesc
	netlink       *nl.NetlinkManager
	logger        logr.Logger
}

func init() {
	registerCollector("netlink", defaultEnabled, NewNetlinkCollector)
}

// NewNetlinkCollector returns a new Collector exposing buddyinfo stats.
func NewNetlinkCollector() (Collector, error) {
	collector := netlinkCollector{
		routesFibDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "netlink", "routes"),
				"The number of routes currently in the Linux Dataplane.",
				[]string{"table", "vrf", "protocol", "address_family"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		neighborsDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "netlink", "neighbors"),
				"The number of neighbors currently in the Linux Dataplane.",
				[]string{"interface", "address_family", "status"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		netlink: &nl.NetlinkManager{},
		logger:  ctrl.Log.WithName("netlink.collector"),
	}

	return &collector, nil
}

func (c *netlinkCollector) updateRoutes(ch chan<- prometheus.Metric) {
	routes, err := c.netlink.ListRouteInformation()
	if err != nil {
		c.logger.Error(err, "cannot get routes from netlink")
	}
	for _, route := range routes {
		ch <- c.routesFibDesc.mustNewConstMetric(float64(route.Fib), fmt.Sprint(route.TableID), route.VrfName, nl.GetProtocolName(route.RouteProtocol), route.AddressFamily)
	}
}

func (c *netlinkCollector) updateNeighbors(ch chan<- prometheus.Metric) {
	neighbors, err := c.netlink.ListNeighborInformation()
	if err != nil {
		c.logger.Error(err, "Cannot get neighbors from netlink")
	}
	for _, neighbor := range neighbors {
		ch <- c.neighborsDesc.mustNewConstMetric(neighbor.Quantity, neighbor.Interface, neighbor.Family, neighbor.State)
	}
}

func (c *netlinkCollector) Update(ch chan<- prometheus.Metric) error {
	c.updateRoutes(ch)
	c.updateNeighbors(ch)
	return nil
}
