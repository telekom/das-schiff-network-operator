package managerconfig

import (
	"crypto/tls"
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// Config holds internal config that is used to create manger.Options struct.
type Config struct {
	Health         HealthCfg         `yaml:"health"`
	Metrics        ServerConfig      `yaml:"metrics"`
	Pprof          ServerConfig      `yaml:"pprof"`
	Webhook        WebhookCfg        `yaml:"webhook"`
	LeaderElection LeaderElectionCfg `yaml:"leaderElection"`
}

// HealthCfg holds internal config for health options of manger.Options struct.
type HealthCfg struct {
	HealthProbeBindAddress string `yaml:"healthProbeBindAddress"`
}

// MetricsCfg holds internal config for metrics options of manger.Options struct.
type ServerConfig struct {
	BindAddress string `yaml:"bindAddress"`
}

// WebhookCfg holds internal config for webhook options of manger.Options struct.
type WebhookCfg struct {
	Port int `yaml:"port"`
}

// LeaderElectionCfg holds internal config for leader election options of manger.Options struct.
type LeaderElectionCfg struct {
	LeaderElect  bool   `yaml:"leaderElect"`
	ResourceName string `yaml:"resourceName"`
}

// Load loads the config yaml from the provided path and converts it to manger.Options struct.
func Load(filepath string, scheme *runtime.Scheme) (manager.Options, error) {
	cm, err := loadConfigMap(filepath)
	if err != nil {
		return manager.Options{}, err
	}
	return prepareManagerOptions(cm, scheme), nil
}

func loadConfigMap(filepath string) (*Config, error) {
	config := &Config{}

	read, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("error reading configfile %s: %w", filepath, err)
	}
	err = yaml.Unmarshal(read, &config)
	if err != nil {
		return nil, fmt.Errorf("error reading unmarshalling config %s: %w", filepath, err)
	}

	return config, nil
}

func prepareManagerOptions(cfg *Config, scheme *runtime.Scheme) manager.Options {
	return manager.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: cfg.Health.HealthProbeBindAddress,
		Metrics: server.Options{
			BindAddress: cfg.Metrics.BindAddress,
		},
		PprofBindAddress: cfg.Pprof.BindAddress,
		WebhookServer: &webhook.DefaultServer{
			Options: webhook.Options{
				Port:    cfg.Webhook.Port,
				CertDir: "/tmp/k8s-webhook-server/serving-certs",
				TLSOpts: []func(c *tls.Config){func(c *tls.Config) { c.MinVersion = tls.VersionTLS13 }},
			},
		},
		LeaderElection:   cfg.LeaderElection.LeaderElect,
		LeaderElectionID: cfg.LeaderElection.ResourceName,
	}
}
