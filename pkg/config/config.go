package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
)

var (
	vniMapFile         = "/opt/network-operator/config.yaml"
	SkipVrfTemplateVni = -1
)

type Config struct {
	VRFToVNI      map[string]int       `yaml:"vnimap"`
	VRFConfig     map[string]VRFConfig `yaml:"vrfConfig"`
	BPFInterfaces []string             `yaml:"bpfInterfaces"`
	SkipVRFConfig []string             `yaml:"skipVRFConfig"`
	ServerASN     int                  `yaml:"serverASN"`
}

type VRFConfig struct {
	VNI int    `yaml:"vni"`
	RT  string `yaml:"rt"`
}

func LoadConfig() (*Config, error) {
	config := &Config{}

	vniFile := vniMapFile
	if val := os.Getenv("OPERATOR_CONFIG"); val != "" {
		vniFile = val
	}

	read, err := os.ReadFile(vniFile)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}
	err = yaml.Unmarshal(read, &config)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling config file: %w", err)
	}

	return config, nil
}

func (c *Config) ShouldSkipVRFConfig(vrf string) bool {
	for _, v := range c.SkipVRFConfig {
		if v == vrf {
			return true
		}
	}
	return false
}
