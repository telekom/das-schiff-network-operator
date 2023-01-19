package monitoring

import (
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/vishvananda/netlink"
	ctrl "sigs.k8s.io/controller-runtime"
)

var ()

type netlinkCollector struct {
	routesDesc typedFactoryDesc
	netlink    *nl.NetlinkManager
	logger     logr.Logger
}

func init() {
	registerCollector("netlink", NewNetlinkCollector)
}

func getFamily(addressFamily int) (string, error) {

	switch addressFamily {
	case netlink.FAMILY_V4:
		return "ipv4", nil
	case netlink.FAMILY_V6:
		return "ipv6", nil
	case netlink.FAMILY_MPLS:
		return "mpls", nil
	case netlink.FAMILY_ALL:
		return "all", nil
	default:
		return "", errors.New("can't find the addressfamily required")
	}
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
		netlink: &nl.NetlinkManager{},
		logger:  ctrl.Log.WithName("netlink.collector"),
	}

	return &collector, nil
}

func (c *netlinkCollector) Update(ch chan<- prometheus.Metric) error {
	routes, err := c.netlink.ListRoutes()
	c.logger.Info("I am in the netlink collector")
	if err != nil {
		return err
	}
	for _, route := range routes {
		ch <- c.routesDesc.mustNewConstMetric(float64(route.Quantity), fmt.Sprint(route.TableId), route.VrfName, route.RouteProtocol.String(), route.AddressFamily)
	}
	return nil
}
