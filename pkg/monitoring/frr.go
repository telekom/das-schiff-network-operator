package monitoring

import (
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/route"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

const secondToMillisecond = 1000
const frrCollectorName = "frr"

type frrCollector struct {
	basicCollector
	routesFibDesc              typedFactoryDesc
	routesRibDesc              typedFactoryDesc
	vrfVniDesc                 typedFactoryDesc
	evpnVniDesc                typedFactoryDesc
	bgpUptimeDesc              typedFactoryDesc
	bgpStatusDesc              typedFactoryDesc
	bgpPrefixesReceivedDesc    typedFactoryDesc
	bgpPrefixesTransmittedDesc typedFactoryDesc
	bgpMessagesReceivedDesc    typedFactoryDesc
	bgpMessagesTransmittedDesc typedFactoryDesc
	frr                        *frr.Manager
}

// as we do not want to have frr collector registered within network operator
// it is commented here.
func init() {
	registerCollector(frrCollectorName, defaultDisabled, NewFRRCollector)
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
		routesFibDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, frrCollectorName, "routes_fib"),
				"The number of routes currently in the frr Controlplane.",
				[]string{"table", "vrf", "protocol", "address_family"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		routesRibDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, frrCollectorName, "routes_rib"),
				"The number of routes currently in the frr Controlplane.",
				[]string{"table", "vrf", "protocol", "address_family"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		vrfVniDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, frrCollectorName, "vni_state"),
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
				prometheus.BuildFQName(namespace, frrCollectorName, "vni_state"),
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
				prometheus.BuildFQName(namespace, frrCollectorName, "bgp_uptime_seconds_total"),
				"Uptime of the session with the other BGP Peer",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpStatusDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, frrCollectorName, "bgp_status"),
				"The Session Status to the other BGP Peer",
				bgpLabels,
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		bgpPrefixesReceivedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, frrCollectorName, "bgp_prefixes_received_total"),
				"The Prefixes Received from the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpPrefixesTransmittedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, frrCollectorName, "bgp_prefixes_transmitted_total"),
				"The Prefixes Transmitted to the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpMessagesReceivedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, frrCollectorName, "bgp_messages_received_total"),
				"The messages Received to the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpMessagesTransmittedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, frrCollectorName, "bgp_messages_transmitted_total"),
				"The messages Transmitted to the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		frr: frr.NewFRRManager(),
	}

	collector.name = frrCollectorName
	collector.logger = ctrl.Log.WithName("frr.collector")

	return &collector, nil
}
func (c *frrCollector) getVrfs() []frr.VrfVniSpec {
	vrfs, err := c.frr.ListVrfs()
	if err != nil {
		c.logger.Error(err, "can't get vrfs from frr")
	}
	return vrfs
}

func (c *frrCollector) updateVrfs(ch chan<- prometheus.Metric, vrfs []frr.VrfVniSpec) {
	for _, vrf := range vrfs {
		// hotfix for default as it is called
		// main in netlink/kernel
		if vrf.Vrf == defaultVRFIntern {
			vrf.Vrf = defaultVRF
			vrf.Table = strconv.Itoa(unix.RT_CLASS_MAIN)
		}
		state := convertToStateFloat(vrf.State)
		ch <- c.vrfVniDesc.mustNewConstMetric(state, vrf.Table, vrf.Vrf, vrf.SviIntf, vrf.VxlanIntf)
	}
}
func (c *frrCollector) getRoutes() []route.Information {
	routeSummaries, err := c.frr.ListRouteSummary("")
	if err != nil {
		c.logger.Error(err, "can't get routes from frr")
	}
	return routeSummaries
}

func (c *frrCollector) updateRoutes(ch chan<- prometheus.Metric, routeSummaries []route.Information) {
	for _, routeSummary := range routeSummaries {
		if routeSummary.VrfName == defaultVRFIntern {
			routeSummary.VrfName = defaultVRF
			routeSummary.TableID = unix.RT_CLASS_MAIN
		}
		ch <- c.routesFibDesc.mustNewConstMetric(float64(routeSummary.Fib), strconv.Itoa(routeSummary.TableID), routeSummary.VrfName, nl.GetProtocolName(routeSummary.RouteProtocol), routeSummary.AddressFamily)
		ch <- c.routesRibDesc.mustNewConstMetric(float64(routeSummary.Rib), strconv.Itoa(routeSummary.TableID), routeSummary.VrfName, nl.GetProtocolName(routeSummary.RouteProtocol), routeSummary.AddressFamily)
	}
}

func (c *frrCollector) getBGPNeighbors() frr.BGPVrfSummary {
	bgpNeighbors, err := c.frr.ListBGPNeighbors("")
	if err != nil {
		c.logger.Error(err, "can't get bgpNeighbors from frr: %w")
	}
	return bgpNeighbors
}

func (c *frrCollector) updateBGPNeighbors(ch chan<- prometheus.Metric, bgpNeighbors frr.BGPVrfSummary) {
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
func (c *frrCollector) updateChannels(vrfs []frr.VrfVniSpec, routes []route.Information, neighbors frr.BGPVrfSummary) {
	for _, ch := range c.channels {
		c.updateVrfs(ch, vrfs)
		c.updateRoutes(ch, routes)
		c.updateBGPNeighbors(ch, neighbors)
	}
}

func (c *frrCollector) Update(ch chan<- prometheus.Metric) error {
	c.mu.Lock()
	c.channels = append(c.channels, ch)
	if len(c.channels) == 1 {
		c.wg = sync.WaitGroup{}
		c.wg.Add(1)
		c.mu.Unlock()

		routes := c.getRoutes()
		vrfs := c.getVrfs()
		neighbors := c.getBGPNeighbors()

		c.mu.Lock()
		c.updateChannels(vrfs, routes, neighbors)
		c.clearChannels()
		c.wg.Done()
		c.mu.Unlock()
	} else {
		c.mu.Unlock()
		c.wg.Wait()
	}
	return nil
}
