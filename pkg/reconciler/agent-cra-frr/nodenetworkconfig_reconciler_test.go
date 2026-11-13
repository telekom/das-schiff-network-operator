package agent_cra_frr //nolint:revive

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

func TestConvertAcceptGratuitousARP(t *testing.T) {
	a := &CRAFRRConfigApplier{baseConfig: &config.BaseConfig{}}
	for _, tt := range []struct {
		name   string
		accept bool
		env    string
		want   bool
	}{
		{name: "default rejects"},
		{name: "network opts in", accept: true, want: true},
		{name: "variable restores old default", env: "always", want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NWOP_L2_ACCEPT_GARP", tt.env)
			cfg := &v1alpha1.NodeNetworkConfig{Spec: v1alpha1.NodeNetworkConfigSpec{
				Layer2s: map[string]v1alpha1.Layer2{"501": {VLAN: 501, AcceptGratuitousARP: tt.accept}},
			}}
			nlCfg := a.convertNodeConfigToNetlink(cfg)
			require.Len(t, nlCfg.Layer2s, 1)
			require.Equal(t, tt.want, nlCfg.Layer2s[0].AcceptGratuitousARP)
		})
	}
}
