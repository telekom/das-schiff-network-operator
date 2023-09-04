package monitoring

import (
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

const secondToMillisecond = 1000

type frrCollector struct {
	routesDesc                 typedFactoryDesc
	vrfVniDesc                 typedFactoryDesc
	evpnVniDesc                typedFactoryDesc
	bgpUptimeDesc              typedFactoryDesc
	bgpStatusDesc              typedFactoryDesc
	bgpPrefixesReceivedDesc    typedFactoryDesc
	bgpPrefixesTransmittedDesc typedFactoryDesc
	bgpMessagesReceivedDesc    typedFactoryDesc
	bgpMessagesTransmittedDesc typedFactoryDesc
	frr                        *frr.Manager
	logger                     logr.Logger
}

func init() {
	registerCollector("frr", NewFRRCollector)
}

func convertToStateFloat(state string) float64 {
	lowerState := strings.ToLower(state)
	if frr.In([]string{"up", "established", "ok", "true"}, lowerState) {
		return 1.0
	}
	return 0.0
}

// NewFRRCollector returns a new Collector exposing buddyinfo stats.
func NewFRRCollector() (Collector, error) {
	bgpLabels := []string{
		"vrf",
		"as",
		"peer_name",
		"peer_host",
		"ip_family",
		"message_type",
		"subsequent_family",
		"remote_as",
	}
	collector := frrCollector{
		routesDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "routes"),
				"The number of routes currently in the frr Controlplane.",
				[]string{"table", "vrf", "protocol", "address_family"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		vrfVniDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "vni_state"),
				"The state of the vrf interface in frr",
				[]string{
					"table", "vrf", "svi", "vtep",
				},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		evpnVniDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "vni_state"),
				"The state of the Evpn vni interface",
				[]string{
					"table", "vrf", "svi", "vtep",
				},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		bgpUptimeDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "bgp_uptime_seconds_total"),
				"Uptime of the session with the other BGP Peer",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpStatusDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "bgp_status"),
				"The Session Status to the other BGP Peer",
				bgpLabels,
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		bgpPrefixesReceivedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "bgp_prefixes_received_total"),
				"The Prefixes Received from the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpPrefixesTransmittedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "bgp_prefixes_transmitted_total"),
				"The Prefixes Transmitted to the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpMessagesReceivedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "bgp_messages_received_total"),
				"The messages Received to the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpMessagesTransmittedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "frr", "bgp_messages_transmitted_total"),
				"The messages Transmitted to the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		frr:    frr.NewFRRManager(),
		logger: ctrl.Log.WithName("frr.collector"),
	}

	return &collector, nil
}

func (c *frrCollector) UpdateVrfs(ch chan<- prometheus.Metric) {
	vrfs, err := c.frr.ListVrfs()
	if err != nil {
		c.logger.Error(err, "Can't get vrfs from frr")
	}
	for _, vrf := range vrfs {
		// hotfix for default as it is called
		// main in netlink/kernel
		if vrf.Vrf == "default" {
			vrf.Vrf = "main"
			vrf.Table = strconv.Itoa(unix.RT_CLASS_MAIN)
		}
		state := convertToStateFloat(vrf.State)
		ch <- c.vrfVniDesc.mustNewConstMetric(state, vrf.Table, vrf.Vrf, vrf.SviIntf, vrf.VxlanIntf)
	}
}

func (c *frrCollector) UpdateRoutes(ch chan<- prometheus.Metric) {
	routes, err := c.frr.ListRoutes("")
	if err != nil {
		c.logger.Error(err, "Can't get routes from frr")
	}
	for _, routePath := range routes {
		if routePath.VrfName == "default" {
			routePath.VrfName = "main"
			routePath.TableID = unix.RT_CLASS_MAIN
		}
		ch <- c.routesDesc.mustNewConstMetric(float64(routePath.Quantity), strconv.Itoa(routePath.TableID), routePath.VrfName, nl.GetProtocolName(routePath.RouteProtocol), routePath.AddressFamily)
	}
}

func (c *frrCollector) UpdateBGPNeighbors(ch chan<- prometheus.Metric) {
	bgpNeighbors, err := c.frr.ListNeighbors("")
	if err != nil {
		c.logger.Error(err, "Can't get bgpNeighbors from frr: %w")
	}

	for _, families := range bgpNeighbors {
		for _, family := range frr.BGPAddressFamilyValues() {
			neighbor, ok := families[family.String()]
			if !ok {
				// this family is not configured
				continue
			}
			for peerName := range neighbor.Peers {
				remoteAs := strconv.Itoa(int(neighbor.Peers[peerName].RemoteAs))
				as := strconv.Itoa(int(neighbor.As))
				bgpLabels := []string{
					neighbor.VrfName,
					as,
					peerName,
					neighbor.Peers[peerName].Hostname,
					neighbor.Peers[peerName].IDType,
					family.Afi(),
					family.Safi(),
					remoteAs,
				}
				upTimeSeconds := float64(neighbor.Peers[peerName].PeerUptimeMsec) / secondToMillisecond
				ch <- c.bgpUptimeDesc.mustNewConstMetric(upTimeSeconds, bgpLabels...)
				ch <- c.bgpStatusDesc.mustNewConstMetric(convertToStateFloat(neighbor.Peers[peerName].State), bgpLabels...)
				ch <- c.bgpPrefixesReceivedDesc.mustNewConstMetric(float64(neighbor.Peers[peerName].PfxRcd), bgpLabels...)
				ch <- c.bgpPrefixesTransmittedDesc.mustNewConstMetric(float64(neighbor.Peers[peerName].PfxSnt), bgpLabels...)
				ch <- c.bgpMessagesReceivedDesc.mustNewConstMetric(float64(neighbor.Peers[peerName].MsgRcvd), bgpLabels...)
				ch <- c.bgpMessagesTransmittedDesc.mustNewConstMetric(float64(neighbor.Peers[peerName].MsgSent), bgpLabels...)
			}
		}
	}
}

func (c *frrCollector) Update(ch chan<- prometheus.Metric) error {
	c.logger.Info("I am in the frr collector")
	c.UpdateVrfs(ch)
	c.UpdateRoutes(ch)
	c.UpdateBGPNeighbors(ch)
	return nil
}
