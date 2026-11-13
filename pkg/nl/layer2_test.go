package nl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigureBridgeArpAccept(t *testing.T) {
	old := procSysNetPath
	procSysNetPath = t.TempDir()
	t.Cleanup(func() { procSysNetPath = old })
	for _, dir := range []string{"ipv4/conf", "ipv4/neigh", "ipv6/neigh"} {
		require.NoError(t, os.MkdirAll(filepath.Join(procSysNetPath, dir, "l2.501"), 0o755))
	}
	arpAccept := filepath.Join(procSysNetPath, "ipv4", "conf", "l2.501", "arp_accept")

	accept, reject := true, false
	for _, tt := range []struct {
		name   string
		accept *bool
		want   string
	}{
		{name: "unset keeps legacy accept", want: "1"},
		{name: "reject", accept: &reject, want: "0"},
		{name: "accept", accept: &accept, want: "1"},
	} {
		require.NoError(t, (&Manager{}).configureBridge("l2.501", tt.accept))
		got, err := os.ReadFile(arpAccept)
		require.NoError(t, err)
		require.Equal(t, tt.want, string(got), tt.name)
	}
}
