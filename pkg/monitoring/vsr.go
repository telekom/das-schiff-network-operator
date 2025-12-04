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
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/cra-vsr"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	vsrTimeout       = 30 * time.Second
	vsrCollectorName = "vsr"
)

type VSRCollector struct {
	basicCollector
	CraManager *cra.Manager
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
	collector := VSRCollector{}

	collector.name = vsrCollectorName
	collector.logger = ctrl.Log.WithName("vsr.collector")

	return &collector, nil
}

func (c *VSRCollector) updateChannels(metrics *cra.Metrics) {
	return
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
