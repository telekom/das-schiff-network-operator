package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
)

const (
	vniMapFile         = "/opt/network-operator/config.yaml"
	SkipVrfTemplateVni = -1
)

type Config struct {
	VRFToVNI                map[string]int       `yaml:"vnimap"`
	VRFConfig               map[string]VRFConfig `yaml:"vrfConfig"`
	BPFInterfaces           []string             `yaml:"bpfInterfaces"`
	SkipVRFConfig           []string             `yaml:"skipVRFConfig"`
	ServerASN               int                  `yaml:"serverASN"`
	IgnoreMissingInterfaces bool                 `yaml:"ignoreMissingInterfaces"`
}

type VRFConfig struct {
	VNI int    `yaml:"vni"`
	RT  string `yaml:"rt"`
}

func LoadConfig() (*Config, error) {
	config := &Config{}

	if err := config.ReloadConfig(); err != nil {
		return nil, err
	}

	return config, nil
}

func (c *Config) ReloadConfig() error {
	vniFile := vniMapFile
	if val := os.Getenv("OPERATOR_CONFIG"); val != "" {
		vniFile = val
	}

	read, err := os.ReadFile(vniFile)
	if err != nil {
		return fmt.Errorf("error reading config file: %w", err)
	}
	err = yaml.Unmarshal(read, c)
	if err != nil {
		return fmt.Errorf("error unmarshalling config file: %w", err)
	}
	return nil
}

func (c *Config) ShouldSkipVRFConfig(vrf string) bool {
	for _, v := range c.SkipVRFConfig {
		if v == vrf {
			return true
		}
	}
	return false
}
