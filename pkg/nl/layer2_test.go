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
	for _, dir := range []string{"ipv4/conf/l2.501", "ipv4/neigh/l2.501", "ipv6/neigh/l2.501"} {
		require.NoError(t, os.MkdirAll(filepath.Join(procSysNetPath, dir), 0o755))
	}
	arpAccept := filepath.Join(procSysNetPath, "ipv4/conf/l2.501/arp_accept")

	for accept, want := range map[bool]string{false: "0", true: "1"} {
		require.NoError(t, (&Manager{}).configureBridge("l2.501", accept))
		got, err := os.ReadFile(arpAccept)
		require.NoError(t, err)
		require.Equal(t, want, string(got), "acceptGARP=%v", accept)
	}
}
