package dbus

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestTraceSetRedactsSecrets(t *testing.T) {
	const secret = "c2VjcmV0LXByaXZhdGUta2V5LWJhc2U2NC1lbmNvZGVkPQ=="
	var out bytes.Buffer
	trace := logrus.New()
	trace.SetOutput(&out)
	config := &Config{path: "/io/netplan/Netplan/config/1", executionLog: trace}

	tunnel := map[string]interface{}{
		"mode": "wireguard",
		"keys": map[string]interface{}{"private": secret},
		"peers": []interface{}{
			map[string]interface{}{"keys": map[string]interface{}{"public": "pub", "shared": secret}, "endpoint": "192.0.2.1:51820"},
		},
	}
	config.traceSet("hint", "network.tunnels.wg0", tunnel)
	config.traceSet("hint", "network.tunnels.wg0.keys.private", secret)
	config.traceSet("hint", "network.tunnels.wg0.keys", map[string]string{"private": secret})
	config.traceSet("hint", "network.wifis.wl0.access-points.ap", map[string]interface{}{"auth": map[string]string{"password": secret, "psk": secret}})

	got := out.String()
	if strings.Contains(got, secret) {
		t.Fatalf("trace contains the secret:\n%s", got)
	}
	for _, want := range []string{`network.tunnels.wg0={`, `\"mode\":\"wireguard\"`, `192.0.2.1:51820`, `<redacted>`} {
		if !strings.Contains(got, want) {
			t.Errorf("trace lacks %q:\n%s", want, got)
		}
	}
}
