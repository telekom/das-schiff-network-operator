package monitoring

import (
	"fmt"
	"sync"

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
	netlink       *nl.Manager
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
		netlink: &nl.Manager{},
	}
	collector.name = nlCollectorName
	collector.logger = ctrl.Log.WithName("netlink.collector")

	return &collector, nil
}

func (c *netlinkCollector) getRoutes() []route.Information {
	routes, err := c.netlink.ListRouteInformation()
	if err != nil {
		c.logger.Error(err, "cannot get routes from netlink")
	}
	return routes
}

func (c *netlinkCollector) updateRoutes(ch chan<- prometheus.Metric, routeSummaries []route.Information) {
	for _, routeSummary := range routeSummaries {
		ch <- c.routesFibDesc.mustNewConstMetric(float64(routeSummary.Fib), fmt.Sprint(routeSummary.TableID), routeSummary.VrfName, nl.GetProtocolName(routeSummary.RouteProtocol), routeSummary.AddressFamily)
	}
}

func (c *netlinkCollector) getNeighbors() []nl.NeighborInformation {
	neighbors, err := c.netlink.ListNeighborInformation()
	if err != nil {
		c.logger.Error(err, "cannot get neighbors from netlink")
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
		c.wg = sync.WaitGroup{}
		c.wg.Add(1)
		c.mu.Unlock()

		routes := c.getRoutes()
		neighbors := c.getNeighbors()

		c.mu.Lock()
		c.updateChannels(neighbors, routes)
		c.clearChannels()
		c.wg.Done()
		c.mu.Unlock()
	} else {
		c.mu.Unlock()
		c.wg.Wait()
	}
	return nil
}
