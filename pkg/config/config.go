package config

import (
	"io/ioutil"
	"os"

	"gopkg.in/yaml.v2"
)

var (
	VNI_MAP_FILE          = "/opt/network-operator/config.yaml"
	SKIP_VRF_TEMPLATE_VNI = -1
)

type Config struct {
	VRFToVNI      map[string]int       `yaml:"vnimap"`
	VRFConfig     map[string]VRFConfig `yaml:"vrfConfig"`
	BPFInterfaces []string             `yaml:"bpfInterfaces"`
	SkipVRFConfig []string             `yaml:"skipVRFConfig"`
}

type VRFConfig struct {
	VNI int `yaml:"vni"`
	RT  int `yaml:"rt"`
}

func LoadConfig() (*Config, error) {
	config := &Config{}

	vniFile := VNI_MAP_FILE
	if val := os.Getenv("OPERATOR_CONFIG"); val != "" {
		vniFile = val
	}

	read, err := ioutil.ReadFile(vniFile)
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(read, &config)
	if err != nil {
		return nil, err
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
