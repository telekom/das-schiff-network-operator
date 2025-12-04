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

package cra

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

const (
	vrfTableStart = 50
	vrfTableEnd   = 80

	bridgePrefix   = "br."
	vxlanPrefix    = "vx."
	layer2SVI      = "l2."
	vlanPrefix     = "vlan."
	loopbackPrefix = "lo."

	underlayInterfaceName = "dum.underlay"

	vxlanPort  = 4789
	defaultMtu = 9000

	baseConfigPath = "/etc/cra/config/base-config.yaml"
)

type Manager struct {
	metricsUrls []string
	baseConfig  *config.BaseConfig
	timeout     time.Duration
	startupXML  []byte
	WorkNS      string
	running     *VRouter
	nc          *Netconf
}

type Metrics struct {
	State VRouter
}

func NewManager(
	urls, metricsUrls []string,
	user, password string,
	timeout time.Duration,
) (*Manager, error) {
	m := &Manager{
		timeout:     timeout,
		metricsUrls: metricsUrls,
		nc:          NewNetconf(urls, user, password, timeout),
	}
	ctx := context.Background()

	baseConfig, err := config.LoadBaseConfig(baseConfigPath)
	if err != nil {
		return nil, fmt.Errorf("error loading base config: %w", err)
	}

	if net.ParseIP(baseConfig.VTEPLoopbackIP).To4() == nil {
		return nil, fmt.Errorf(
			"VTEPLoopbackIP is not IPv4 in base config: %s",
			baseConfig.VTEPLoopbackIP,
		)
	}
	m.baseConfig = baseConfig

	err = m.nc.Open(ctx)
	if err != nil {
		return nil, err
	}

	m.startupXML, err = m.nc.Get(ctx, Startup, "/config")
	if err != nil {
		return nil, err
	}

	m.running = &VRouter{}
	err = m.nc.GetUnmarshal(ctx, Running, "/config", m.running)
	if err != nil {
		return nil, err
	}

	m.WorkNS, err = m.findWorkNS(m.running)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (m *Manager) findWorkNS(vrouter *VRouter) (string, error) {
	for _, ns := range vrouter.Namespaces {
		if ns.Interfaces != nil {
			for _, infra := range ns.Interfaces.Infras {
				if infra.Name == m.baseConfig.TrunkInterfaceName {
					return ns.Name, nil
				}
			}
		}
		for _, vrf := range ns.VRFs {
			for _, infra := range vrf.Interfaces.Infras {
				if infra.Name == m.baseConfig.TrunkInterfaceName {
					return ns.Name, nil
				}
			}
		}
	}

	return "", fmt.Errorf("failed to found working NetNS")
}

func (m *Manager) isReservedVRF(name string) bool {
	return name == m.baseConfig.ManagementVRF.Name || name == m.baseConfig.ClusterVRF.Name
}

func (m *Manager) ApplyConfiguration(
	ctx context.Context,
	nodeCfg *v1alpha1.NodeNetworkConfigSpec,
) error {
	err := m.nc.Edit(ctx, Candidate, Replace, m.startupXML)
	if err != nil {
		return err
	}

	vrouter, err := m.makeVRouter(nodeCfg)
	if err != nil {
		return err
	}

	err = m.nc.Edit(ctx, Candidate, Merge, &VRouterConfig{VRouter: *vrouter})
	if err != nil {
		return err
	}

	err = m.nc.Commit(ctx)
	if err != nil {
		return err
	}

	m.running = vrouter
	return nil
}

func (m *Manager) makeVRouter(nodeCfg *v1alpha1.NodeNetworkConfigSpec) (*VRouter, error) {
	vrouter := &VRouter{
		Routing: &GlobalRouting{
			NCOperation: Replace,
			BGP:         &GlobalBGP{},
		},
	}

	ns := Namespace{
		Name:       m.WorkNS,
		Interfaces: &Interfaces{},
		Routing: &Routing{
			NCOperation: Replace,
			BGP:         &BGP{},
		},
	}

	l3 := NewLayer3(nodeCfg, &ns, m)
	if err := l3.setup(); err != nil {
		return nil, err
	}

	l2 := NewLayer2(nodeCfg, &ns, m)
	if err := l2.setup(); err != nil {
		return nil, err
	}

	bgp := NewLayerBGP(nodeCfg, vrouter, &ns, m)
	if err := bgp.setup(); err != nil {
		return nil, err
	}

	vrouter.Namespaces = append(vrouter.Namespaces, ns)

	return vrouter, nil
}

func (*Manager) xpath(prefix string, paths []string) string {
	for i := range paths {
		paths[i] = prefix + paths[i]
	}
	return strings.Join(paths, " | ")
}

func (m *Manager) GetMetrics(ctx context.Context) (*Metrics, error) {
	metrics := Metrics{
		State: VRouter{},
	}

	xpath := m.xpath("/state/vrf[name='"+m.WorkNS+"']", []string{
		"/routing/evpn",
		"/l3vrf/table-id",
		"/l3vrf/routing/evpn",
	})
	err := m.nc.GetUnmarshal(ctx, Operational, xpath, &metrics.State)
	if err != nil {
		return nil, fmt.Errorf("get-state failed in metrics: %w", err)
	}

	return &metrics, nil
}
