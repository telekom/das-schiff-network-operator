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
	urls       []string
	baseConfig *config.BaseConfig
}

func NewManager(urls []string, user, password string, timeout time.Duration) (*Manager, error) {
	m := &Manager{
		urls: urls,
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

	return m, nil
}

func (m *Manager) ApplyConfiguration(
	ctx context.Context,
	nodeCfg *v1alpha1.NodeNetworkConfigSpec,
) error {
	return nil
}
