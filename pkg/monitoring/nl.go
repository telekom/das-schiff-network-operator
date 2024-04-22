package monitoring

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/route"
	ctrl "sigs.k8s.io/controller-runtime"
)

const nlCollectorName = "netlink"

type netlinkCollector struct {
	basicCollector
	routesFibDesc typedFactoryDesc
	neighborsDesc typedFactoryDesc
	netlink       *nl.NetlinkManager
}

func init() {
	registerCollector(nlCollectorName, defaultEnabled, NewNetlinkCollector)
}

// NewNetlinkCollector returns a new Collector exposing buddyinfo stats.
func NewNetlinkCollector() (Collector, error) {
	collector := netlinkCollector{
		routesFibDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, nlCollectorName, "routes_fib"),
				"The number of routes currently in the Linux Dataplane.",
				[]string{"table", "vrf", "protocol", "address_family"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		neighborsDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, nlCollectorName, "neighbors"),
				"The number of neighbors currently in the Linux Dataplane.",
				[]string{"interface", "address_family", "flags", "status"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		netlink: &nl.NetlinkManager{},
	}
	collector.name = nlCollectorName
	collector.logger = ctrl.Log.WithName("netlink.collector")

	return &collector, nil
}

func (c *netlinkCollector) getRoutes() []route.Information {
	c.mu.Lock()
	defer c.mu.Unlock()
	routes, err := c.netlink.ListRouteInformation()
	if err != nil {
		c.logger.Error(err, "cannot get routes from netlink")
	}
	return routes
}

func (c *netlinkCollector) updateRoutes(ch chan<- prometheus.Metric, routes []route.Information) {
	for _, route := range routes {
		ch <- c.routesFibDesc.mustNewConstMetric(float64(route.Fib), fmt.Sprint(route.TableID), route.VrfName, nl.GetProtocolName(route.RouteProtocol), route.AddressFamily)
	}
}

func (c *netlinkCollector) getNeighbors() []nl.NeighborInformation {
	c.mu.Lock()
	defer c.mu.Unlock()
	neighbors, err := c.netlink.ListNeighborInformation()
	if err != nil {
		c.logger.Error(err, "Cannot get neighbors from netlink")
	}
	return neighbors
}

func (c *netlinkCollector) updateNeighbors(ch chan<- prometheus.Metric, neighbors []nl.NeighborInformation) {
	for _, neighbor := range neighbors {
		ch <- c.neighborsDesc.mustNewConstMetric(neighbor.Quantity, neighbor.Interface, neighbor.Family, neighbor.Flag, neighbor.State)
	}
}

func (c *netlinkCollector) updateChannels(neighbors []nl.NeighborInformation, routes []route.Information) {
	for _, ch := range c.channels {
		c.updateNeighbors(ch, neighbors)
		c.updateRoutes(ch, routes)
	}
}

func (c *netlinkCollector) Update(ch chan<- prometheus.Metric) error {
	c.mu.Lock()
	c.channels = append(c.channels, ch)
	if len(c.channels) == 1 {
		go func() {
			routes := c.getRoutes()
			neighbors := c.getNeighbors()
			c.mu.Lock()
			defer c.mu.Unlock()
			c.updateChannels(neighbors, routes)
			c.clearChannels()
			close(c.done)
			c.done = make(chan struct{})
		}()
	}
	c.mu.Unlock()
	c.waitUntilDone()
	return nil
}
