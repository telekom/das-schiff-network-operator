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
	VRFToVNI  map[string]int       `yaml:"vnimap"`
	VRFConfig map[string]VRFConfig `yaml:"vrfConfig"`
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

func (c *Config) GetVNIAndRT(vrf string) (int, string, error) {
	if vrfConfig, ok := c.VRFConfig[vrf]; ok {
		return vrfConfig.VNI, vrfConfig.RT, nil
	}
	if vni, ok := c.VRFToVNI[vrf]; ok {
		return vni, "", nil
	}
	return 0, "", fmt.Errorf("vrf %s not found in config", vrf)
}
