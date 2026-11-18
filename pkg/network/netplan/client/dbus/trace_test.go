package dbus

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// marshalCounter counts how often the trace encodes it.
type marshalCounter struct{ calls *int }

func (m marshalCounter) MarshalJSON() ([]byte, error) {
	*m.calls++
	return json.Marshal("v")
}

func TestTraceSetSkipsWorkWhenTraceDisabled(t *testing.T) {
	var out bytes.Buffer
	trace := logrus.New()
	trace.SetOutput(&out)
	trace.SetLevel(logrus.FatalLevel) // as client.New without ExecutionLogPath
	config := &Config{path: "/io/netplan/Netplan/config/1", executionLog: trace}

	calls := 0
	config.traceSet("hint", "network.ethernets.eth0", marshalCounter{calls: &calls})
	if calls != 0 || out.Len() != 0 {
		t.Fatalf("disabled trace encoded the value %d times and wrote %q", calls, out.String())
	}
}

func TestTraceSetRedactsSecrets(t *testing.T) {
	const secret = "c2VjcmV0LXByaXZhdGUta2V5LWJhc2U2NC1lbmNvZGVkPQ==" //nolint:gosec // test value, not a credential
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
