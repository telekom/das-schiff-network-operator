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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/cra-vsr"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/slice"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	vsrTimeout       = 30 * time.Second
	vsrCollectorName = "vsr"
)

type VSRCollector struct {
	basicCollector
	CraManager *cra.Manager
	vrfVniDesc typedFactoryDesc
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
	}

	collector.name = vsrCollectorName
	collector.logger = ctrl.Log.WithName("vsr.collector")

	return &collector, nil
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

func (c *VSRCollector) updateChannels(metrics *cra.Metrics) {
	for _, ch := range c.channels {
		c.updateVRFs(ch, metrics)
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
