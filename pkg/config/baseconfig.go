package config

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v2"
)

type BaseConfig struct {
	VTEPLoopbackIP     string `yaml:"vtepLoopbackIP"`
	TrunkInterfaceName string `yaml:"trunkInterfaceName"`
	MgmtInterfaceName  string `yaml:"mgmtInterfaceName"`
	// CRANetns optionally pins the CRA network namespace used by the routed CNI
	// (cni-routed). Accepted values: "" or "auto" (auto-discover the namespace
	// owning TrunkInterfaceName), a named namespace under /var/run/netns/<name>,
	// or an absolute netns path. When empty the CNI auto-discovers by trunk.
	CRANetns          string     `yaml:"craNetns,omitempty"`
	ExportCIDRs       []string   `yaml:"exportCIDRs"`
	ManagementVRF     BaseVRF    `yaml:"managementVRF"`
	ClusterVRF        BaseVRF    `yaml:"clusterVRF"`
	LocalASN          int        `yaml:"localASN"`
	UnderlayNeighbors []Neighbor `yaml:"underlayNeighbors"`
	ClusterNeighbors  []Neighbor `yaml:"clusterNeighbors"`
}

// MgmtInterface returns the management interface name, falling back to the
// trunk interface when no dedicated management interface is configured.
func (c *BaseConfig) MgmtInterface() string {
	if c.MgmtInterfaceName != "" {
		return c.MgmtInterfaceName
	}
	return c.TrunkInterfaceName
}

type BaseVRF struct {
	Name            string `yaml:"name"`
	VNI             int    `yaml:"vni"`
	EVPNRouteTarget string `yaml:"evpnRouteTarget"`
}

type Neighbor struct {
	IP        *string `yaml:"ip"`
	Interface *string `yaml:"interface"`

	UpdateSource *string `yaml:"updateSource"`

	RemoteASN string  `yaml:"remoteASN"`
	LocalASN  *string `yaml:"localASN"`

	KeepaliveTime int `yaml:"keepaliveTime"`
	HoldTime      int `yaml:"holdTime"`

	BFDMinTimer *int `yaml:"bfdMinTimer"`

	IPv4 bool `yaml:"ipv4"`
	IPv6 bool `yaml:"ipv6"`
	EVPN bool `yaml:"evpn"`
}

func LoadBaseConfig(path string) (*BaseConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open base config file: %w", err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read base config file: %w", err)
	}

	var baseConfig BaseConfig

	if err := yaml.Unmarshal(content, &baseConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal base config: %w", err)
	}

	return &baseConfig, nil
}
