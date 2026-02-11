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
	"encoding/xml"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/types"
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
	WorkNSName  string
	KpiNSName   string
	startup     *VRouter
	running     *VRouter
	nc          *Netconf
}

type Metrics struct {
	State            VRouter
	V4RouteSummaries map[string]ShowRouteSummaryOutput
	V6RouteSummaries map[string]ShowRouteSummaryOutput
	Neighbors        ShowNeighborsOutput
	BridgeFDB        ShowBridgeFDBOutput
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

	m.startup = &VRouter{}
	if err := xml.Unmarshal(m.startupXML, m.startup); err != nil {
		return nil, fmt.Errorf(
			"failed to un-marshal startup config=%s: %w",
			m.startupXML, err)
	}

	m.running = &VRouter{}
	err = m.nc.GetUnmarshal(ctx, Running, "/config", m.running)
	if err != nil {
		return nil, err
	}

	m.WorkNSName, err = m.findWorkNSName(m.startup)
	if err != nil {
		return nil, err
	}

	m.KpiNSName = m.findKpiNSName(m.startup)

	return m, nil
}

func (m *Manager) findWorkNSName(vrouter *VRouter) (string, error) {
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

func (*Manager) findKpiNSName(vrouter *VRouter) string {
	for _, ns := range vrouter.Namespaces {
		if ns.KPI == nil {
			continue
		}
		if ns.KPI.Telegraf == nil || !ns.KPI.Telegraf.Enabled {
			continue
		}
		if ns.KPI.Telegraf.Metrics == nil || !ns.KPI.Telegraf.Metrics.Enabled {
			continue
		}
		return ns.Name
	}

	return ""
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
		Name:       m.WorkNSName,
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

	m.setupKPI(vrouter, &ns)

	vrouter.Namespaces = append(vrouter.Namespaces, ns)

	return vrouter, nil
}

func (m *Manager) setupKPI(vrouter *VRouter, newNS *Namespace) {
	if m.KpiNSName == "" {
		return
	}

	kpiNS := LookupNS(vrouter, m.KpiNSName)
	if kpiNS == nil {
		vrouter.Namespaces = append(vrouter.Namespaces, Namespace{
			Name: m.KpiNSName,
		})
		kpiNS = LookupNS(vrouter, m.KpiNSName)
	}

	if kpiNS.KPI == nil {
		kpiNS.KPI = &KPI{
			Telegraf: &Telegraf{
				Enabled: true,
				Metrics: &TelegrafMetrics{
					Enabled: true,
				},
			},
		}
	}

	setupMonitoredIntfs := func(nsName string, intfs *Interfaces) {
		if intfs == nil {
			return
		}

		metrics := kpiNS.KPI.Telegraf.Metrics
		for i := range intfs.Physicals {
			phys := &intfs.Physicals[i]
			metrics.MonitoredIntfs = append(metrics.MonitoredIntfs, MonitoredIntf{
				Name:      phys.Name,
				Namespace: nsName,
			})
		}
		for i := range intfs.Bridges {
			br := &intfs.Bridges[i]
			metrics.MonitoredIntfs = append(metrics.MonitoredIntfs, MonitoredIntf{
				Name:      br.Name,
				Namespace: nsName,
			})
		}
		for i := range intfs.VXLANs {
			vx := &intfs.VXLANs[i]
			metrics.MonitoredIntfs = append(metrics.MonitoredIntfs, MonitoredIntf{
				Name:      vx.Name,
				Namespace: nsName,
			})
		}
		for i := range intfs.VLANs {
			vl := &intfs.VLANs[i]
			metrics.MonitoredIntfs = append(metrics.MonitoredIntfs, MonitoredIntf{
				Name:      vl.Name,
				Namespace: nsName,
			})
		}
	}

	for _, ns := range []*Namespace{
		LookupNS(m.startup, "main"),
		LookupNS(m.startup, m.WorkNSName),
		newNS,
	} {
		if ns == nil {
			continue
		}
		setupMonitoredIntfs(ns.Name, ns.Interfaces)
		for _, vrf := range ns.VRFs {
			setupMonitoredIntfs(ns.Name, vrf.Interfaces)
		}
	}
}

func (*Manager) xpath(prefix string, paths []string) string {
	for i := range paths {
		paths[i] = prefix + paths[i]
	}
	return strings.Join(paths, " | ")
}

func (m *Manager) GetMetrics(ctx context.Context) (*Metrics, error) {
	metrics := Metrics{
		State:            VRouter{},
		V4RouteSummaries: map[string]ShowRouteSummaryOutput{},
		V6RouteSummaries: map[string]ShowRouteSummaryOutput{},
		Neighbors:        ShowNeighborsOutput{},
		BridgeFDB:        ShowBridgeFDBOutput{},
	}

	xpath := m.xpath("/state/vrf[name='"+m.WorkNSName+"']", []string{
		"/routing/evpn",
		"/routing/bgp/as",
		"/routing/bgp/neighbor",
		"/routing/bgp/neighbor-group",
		"/routing/bgp/unnumbered-neighbor",
		"/l3vrf/table-id",
		"/l3vrf/routing/evpn",
		"/l3vrf/routing/bgp/as",
		"/l3vrf/routing/bgp/neighbor",
		"/l3vrf/routing/bgp/neighbor-group",
		"/l3vrf/routing/bgp/unnumbered-neighbor",
	})
	err := m.nc.GetUnmarshal(ctx, Operational, xpath, &metrics.State)
	if err != nil {
		return nil, fmt.Errorf("get-state failed in metrics: %w", err)
	}

	workns := LookupNS(&metrics.State, m.WorkNSName)
	if workns == nil {
		return nil, fmt.Errorf("work-ns not found in metrics state")
	}

	vrfList := []string{"default"}
	for i := range workns.VRFs {
		vrfList = append(vrfList, workns.VRFs[i].Name)
	}

	for _, name := range vrfList {
		req4 := ShowIPv4RouteSummaryInput{
			Namespace: &m.WorkNSName,
			VRF:       types.ToPtr(name),
		}
		out := ShowRouteSummaryOutput{}
		if err := m.nc.RPC(ctx, &req4, &out); err != nil {
			return nil, fmt.Errorf("show-ipv4-route-summary failed in metrics: %w", err)
		}
		metrics.V4RouteSummaries[name] = out

		req6 := ShowIPv6RouteSummaryInput{
			Namespace: &m.WorkNSName,
			VRF:       types.ToPtr(name),
		}
		out = ShowRouteSummaryOutput{}
		if err := m.nc.RPC(ctx, &req6, &out); err != nil {
			return nil, fmt.Errorf("show-ipv6-route-summary failed in metrics: %w", err)
		}
		metrics.V6RouteSummaries[name] = out
	}

	{
		req := ShowNeighborsInput{
			Namespace: &m.WorkNSName,
		}
		if err := m.nc.RPC(ctx, &req, &metrics.Neighbors); err != nil {
			return nil, fmt.Errorf("show-neighbors failed in metrics: %w", err)
		}
	}

	{
		req := ShowBridgeFDBInput{
			Namespace: &m.WorkNSName,
		}
		if err := m.nc.RPC(ctx, &req, &metrics.BridgeFDB); err != nil {
			return nil, fmt.Errorf("show-bridge-fdb failed in metrics: %w", err)
		}
	}

	return &metrics, nil
}
