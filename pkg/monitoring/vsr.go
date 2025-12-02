/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package monitoring

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/cra-vsr"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/slice"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	vsrTimeout       = 30 * time.Second
	vsrCollectorName = "vsr"
)

type VSRCollector struct {
	basicCollector
	CraManager                 *cra.Manager
	vrfVniDesc                 typedFactoryDesc
	bgpUptimeDesc              typedFactoryDesc
	bgpStatusDesc              typedFactoryDesc
	bgpPrefixesReceivedDesc    typedFactoryDesc
	bgpPrefixesTransmittedDesc typedFactoryDesc
	bgpMessagesReceivedDesc    typedFactoryDesc
	bgpMessagesTransmittedDesc typedFactoryDesc
	routesFibDesc              typedFactoryDesc
	routesRibDesc              typedFactoryDesc
}

type BGPNeighborInfo struct {
	vrfName        string
	bgpAS          string
	neighName      string
	idType         string
	remoteAS       string
	uptime         string
	state          string
	pfxrcd, pfxsnd int
	msgrcd, msgsnd int
}

func init() {
	registerCollector(vsrCollectorName, defaultDisabled, NewVSRCollector)
}

func NewVSRCollector() (Collector, error) {
	bgpLabels := []string{
		"vrf",
		"as",
		"peer_name",
		"ip_family",
		"message_type",
		"subsequent_family",
		"remote_as",
	}

	collector := VSRCollector{
		vrfVniDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, vsrCollectorName, "vni_state"),
				"The state of the vrf interface in frr",
				[]string{
					"table", "vrf", "svi", "vtep",
				},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		bgpUptimeDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, vsrCollectorName, "bgp_uptime_seconds_total"),
				"Uptime of the session with the other BGP Peer",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpStatusDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, vsrCollectorName, "bgp_status"),
				"The Session Status to the other BGP Peer",
				bgpLabels,
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		bgpPrefixesReceivedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, vsrCollectorName, "bgp_prefixes_received_total"),
				"The Prefixes Received from the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpPrefixesTransmittedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, vsrCollectorName, "bgp_prefixes_transmitted_total"),
				"The Prefixes Transmitted to the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpMessagesReceivedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, vsrCollectorName, "bgp_messages_received_total"),
				"The messages Received to the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		bgpMessagesTransmittedDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, vsrCollectorName, "bgp_messages_transmitted_total"),
				"The messages Transmitted to the other peer.",
				bgpLabels,
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		routesFibDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, vsrCollectorName, "routes_fib"),
				"The number of routes currently in the frr Controlplane.",
				[]string{"table", "vrf", "protocol", "address_family"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
		routesRibDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, vsrCollectorName, "routes_rib"),
				"The number of routes currently in the frr Controlplane.",
				[]string{"table", "vrf", "protocol", "address_family"},
				nil,
			),
			valueType: prometheus.GaugeValue,
		},
	}

	collector.name = vsrCollectorName
	collector.logger = ctrl.Log.WithName("vsr.collector")

	return &collector, nil
}

//nolint:mnd
func (*VSRCollector) isIPv6(address string) bool {
	return strings.Count(address, ":") >= 2
}

func (*VSRCollector) convertToStateFloat(state string) float64 {
	lowerState := strings.ToLower(state)
	if slice.ContainsString([]string{"up", "established", "ok", "true"}, lowerState) {
		return 1.0
	}
	return 0.0
}

func (c *VSRCollector) updateVRF(
	ch chan<- prometheus.Metric, name string, tid int, rt *cra.Routing,
) {
	state := 0.0
	vtep := ""
	svi := ""

	if rt == nil {
		return
	}

	if rt != nil && rt.RoutingState != nil {
		for i := range rt.EVPN.VNIs {
			vni := &rt.EVPN.VNIs[i]
			if vni.Type != "L3" {
				continue
			}
			state = c.convertToStateFloat(vni.State)
			vtep = vni.VXLAN
			svi = vni.SVI
		}
	}

	ch <- c.vrfVniDesc.mustNewConstMetric(state, strconv.Itoa(tid), name, svi, vtep)
}

func (c *VSRCollector) updateVRFs(ch chan<- prometheus.Metric, metrics *cra.Metrics) {
	workns := cra.LookupNS(&metrics.State, c.CraManager.WorkNS)
	if workns == nil {
		return
	}

	c.updateVRF(ch, defaultVRF, unix.RT_CLASS_MAIN, workns.Routing)
	for _, vrf := range workns.VRFs {
		c.updateVRF(ch, vrf.Name, vrf.TableID, vrf.Routing)
	}
}

func (*VSRCollector) findBGP(rt *cra.Routing) *cra.BGP {
	if rt != nil {
		return rt.BGP
	}

	return nil
}

func (*VSRCollector) findBGPNeighborGroup(name string, bgp *cra.BGP) *cra.BGPNeighborGroup {
	for i := range bgp.NeighGroups {
		if bgp.NeighGroups[i].Name == name {
			return &bgp.NeighGroups[i]
		}
	}

	return nil
}

func (c *VSRCollector) updateBGPNeighborAF(
	ch chan<- prometheus.Metric,
	info *BGPNeighborInfo,
	afi, safi string,
) {
	labels := []string{
		info.vrfName,
		info.bgpAS,
		info.neighName,
		info.idType,
		afi,
		safi,
		info.remoteAS,
	}

	value := 0.0
	if uptime, err := time.Parse(time.RFC3339, info.uptime); err == nil {
		value = float64(int(time.Since(uptime).Seconds()))
	}
	ch <- c.bgpUptimeDesc.mustNewConstMetric(value, labels...)

	value = convertToStateFloat(info.state)
	ch <- c.bgpStatusDesc.mustNewConstMetric(value, labels...)

	value = float64(info.pfxrcd)
	ch <- c.bgpPrefixesReceivedDesc.mustNewConstMetric(value, labels...)

	value = float64(info.pfxsnd)
	ch <- c.bgpPrefixesTransmittedDesc.mustNewConstMetric(value, labels...)

	value = float64(info.msgrcd)
	ch <- c.bgpMessagesReceivedDesc.mustNewConstMetric(value, labels...)

	value = float64(info.msgsnd)
	ch <- c.bgpMessagesTransmittedDesc.mustNewConstMetric(value, labels...)
}

func (c *VSRCollector) updateBGPNeighborAFs(
	ch chan<- prometheus.Metric,
	vrfName string,
	bgp *cra.BGP,
	neigh any,
) {
	var neighConfig *cra.BGPNeighbor
	var neighState *cra.BGPNeighbor
	var groupName *string

	info := &BGPNeighborInfo{
		vrfName: vrfName,
		bgpAS:   bgp.AS,
	}

	switch v := neigh.(type) {
	case *cra.BGPNeighborIP:
		info.neighName = v.Address
		if c.isIPv6(v.Address) {
			info.idType = "ipv6"
		} else {
			info.idType = "ipv4"
		}
		groupName = v.NeighGroup
		neighState = &v.BGPNeighbor
	case *cra.BGPNeighborIF:
		info.neighName = v.Interface
		info.idType = "interface"
		groupName = v.NeighGroup
		neighState = &v.BGPNeighbor
	default:
		return
	}

	if neighState.BGPNeighborState == nil {
		return
	}

	if groupName != nil {
		neighConfig := c.findBGPNeighborGroup(*groupName, bgp)
		if neighConfig == nil {
			return
		}
	} else {
		neighConfig = neighState
	}

	if neighConfig.RemoteAS != nil {
		info.remoteAS = *neighConfig.RemoteAS
	}

	info.state = neighState.State
	info.uptime = neighState.EstablishmentDate
	info.msgrcd = neighState.Statistics.TotalRecv
	info.msgsnd = neighState.Statistics.TotalSent

	if neighState.AF != nil {
		af := neighState.AF
		if af.UcastV4 != nil && af.UcastV4.BGPNeighAFState != nil {
			info.pfxrcd = af.UcastV4.PrefixAccepted
			info.pfxsnd = af.UcastV4.PrefixSent
			c.updateBGPNeighborAF(ch, info, "ipv4", "unicast")
		}
		if af.UcastV6 != nil && af.UcastV6.BGPNeighAFState != nil {
			info.pfxrcd = af.UcastV6.PrefixAccepted
			info.pfxsnd = af.UcastV6.PrefixSent
			c.updateBGPNeighborAF(ch, info, "ipv6", "unicast")
		}
		if af.EVPN != nil && af.EVPN.BGPNeighAFState != nil {
			info.pfxrcd = af.EVPN.PrefixAccepted
			info.pfxsnd = af.EVPN.PrefixSent
			c.updateBGPNeighborAF(ch, info, "l2vpn", "evpn")
		}
	}
}

func (c *VSRCollector) updateBGPNeighbors(ch chan<- prometheus.Metric, metrics *cra.Metrics) {
	workns := cra.LookupNS(&metrics.State, c.CraManager.WorkNS)
	if workns == nil {
		return
	}

	bgp := c.findBGP(workns.Routing)
	if bgp != nil {
		for i := range bgp.NeighborIPs {
			c.updateBGPNeighborAFs(ch, defaultVRF, bgp, &bgp.NeighborIPs[i])
		}
		for i := range bgp.NeighborIFs {
			c.updateBGPNeighborAFs(ch, defaultVRF, bgp, &bgp.NeighborIFs[i])
		}
	}

	for k := range workns.VRFs {
		bgp := c.findBGP(workns.VRFs[k].Routing)
		if bgp == nil {
			continue
		}

		vrf := &workns.VRFs[k]
		for i := range bgp.NeighborIPs {
			c.updateBGPNeighborAFs(ch, vrf.Name, bgp, &bgp.NeighborIPs[i])
		}
		for i := range bgp.NeighborIFs {
			c.updateBGPNeighborAFs(ch, vrf.Name, bgp, &bgp.NeighborIFs[i])
		}
	}
}

func (*VSRCollector) updateRouteQuantities(
	routes []cra.ShowRouteSummaryProtocol,
) []cra.ShowRouteSummaryProtocol {
	newRoutes := map[string]cra.ShowRouteSummaryProtocol{}

	for _, route := range routes {
		protocol := nl.GetProtocolName(
			netlink.RouteProtocol(nl.GetProtocolNumber(route.Protocol, true)),
		)

		if newRoute, ok := newRoutes[protocol]; ok {
			newRoutes[protocol] = cra.ShowRouteSummaryProtocol{
				Protocol: protocol,
				FIB:      newRoute.FIB + route.FIB,
				RIB:      newRoute.RIB + route.RIB,
			}
			continue
		}

		newRoutes[protocol] = cra.ShowRouteSummaryProtocol{
			Protocol: protocol,
			RIB:      route.RIB,
			FIB:      route.FIB,
		}
	}

	return slices.Collect(maps.Values(newRoutes))
}

func (c *VSRCollector) updateRoute(
	ch chan<- prometheus.Metric,
	workns *cra.Namespace,
	vrfName string,
	route *cra.ShowRouteSummaryProtocol,
	af string,
) {
	var tableID int

	if vrfName != defaultVRFIntern {
		vrf := cra.LookupVRF(workns, vrfName)
		if vrf == nil {
			return
		}
		tableID = vrf.TableID
	} else {
		vrfName = defaultVRF
		tableID = unix.RT_CLASS_MAIN
	}

	ch <- c.routesFibDesc.mustNewConstMetric(
		float64(route.FIB), strconv.Itoa(tableID), vrfName, route.Protocol, af)
	ch <- c.routesRibDesc.mustNewConstMetric(
		float64(route.RIB), strconv.Itoa(tableID), vrfName, route.Protocol, af)
}

func (c *VSRCollector) updateRoutes(ch chan<- prometheus.Metric, metrics *cra.Metrics) {
	workns := cra.LookupNS(&metrics.State, c.CraManager.WorkNS)
	if workns == nil {
		return
	}

	for name, summary := range metrics.V4RouteSummaries {
		routes := c.updateRouteQuantities(summary.Routes)
		for i := range routes {
			c.updateRoute(ch, workns, name, &routes[i], "ipv4")
		}
	}

	for name, summary := range metrics.V6RouteSummaries {
		routes := c.updateRouteQuantities(summary.Routes)
		for i := range routes {
			c.updateRoute(ch, workns, name, &routes[i], "ipv6")
		}
	}
}

func (c *VSRCollector) updateChannels(metrics *cra.Metrics) {
	for _, ch := range c.channels {
		c.updateVRFs(ch, metrics)
		c.updateRoutes(ch, metrics)
		c.updateBGPNeighbors(ch, metrics)
	}
}

func (c *VSRCollector) Update(ch chan<- prometheus.Metric) error {
	if c.CraManager == nil {
		return fmt.Errorf("cra-vsr manager not defined in vsr collector")
	}

	c.mu.Lock()
	c.channels = append(c.channels, ch)
	if len(c.channels) == 1 {
		c.wg = sync.WaitGroup{}
		c.wg.Add(1)
		c.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), vsrTimeout)
		defer cancel()

		metrics, err := c.CraManager.GetMetrics(ctx)
		if err != nil {
			return fmt.Errorf("update of vsr collector failed: %w", err)
		}

		c.mu.Lock()
		c.updateChannels(metrics)
		c.clearChannels()
		c.wg.Done()
		c.mu.Unlock()
	} else {
		c.mu.Unlock()
		c.wg.Wait()
	}

	return nil
}
