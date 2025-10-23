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
	"time"

	"github.com/nemith/netconf"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"golang.org/x/crypto/ssh"
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
	urls       []string
	sshConfig  ssh.ClientConfig
	baseConfig *config.BaseConfig
	timeout    time.Duration
	startupXML []byte
	workNS     string
	running    *VRouter
	nc         Netconf
}

func NewManager(urls []string, user, password string, timeout time.Duration) (*Manager, error) {
	m := &Manager{
		urls:    urls,
		timeout: timeout,
		sshConfig: ssh.ClientConfig{
			User: user,
			Auth: []ssh.AuthMethod{
				ssh.Password(password),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		},
	}

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

	if err := m.openSession(context.Background()); err != nil {
		return nil, err
	}

	return m, nil
}

func (m *Manager) openSession(ctx context.Context) error {
	var err error

	if err = m.nc.openSession(ctx, m.urls, m.timeout, &m.sshConfig); err != nil {
		return err
	}

	m.startupXML, err = m.nc.getConfig(ctx, netconf.Startup)
	if err != nil {
		return err
	}

	m.running, err = m.nc.getVRouter(ctx, netconf.Running)
	if err != nil {
		return err
	}

	m.workNS, err = m.findWorkNS(m.running)
	if err != nil {
		return err
	}

	return nil
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

func (m *Manager) ApplyConfiguration(
	ctx context.Context,
	nodeCfg *v1alpha1.NodeNetworkConfigSpec,
) error {
	return nil
}
