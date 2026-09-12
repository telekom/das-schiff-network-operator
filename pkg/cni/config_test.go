/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cni

import (
	"os"
	"path/filepath"
	"testing"
)

const validConf = `{
  "cniVersion": "1.0.0",
  "name": "routed",
  "type": "cni-workload",
  "vrf": "cluster",
  "ipam": { "type": "host-local", "ranges": [[{"subnet":"10.100.0.0/24"}]] }
}`

func TestParseConfigValid(t *testing.T) {
	conf, err := parseConfig([]byte(validConf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf.VRF != "cluster" {
		t.Errorf("VRF = %q, want cluster", conf.VRF)
	}
	if conf.mtu() != defaultMTU {
		t.Errorf("mtu() = %d, want %d", conf.mtu(), defaultMTU)
	}
	if conf.trunkInterface() != "hbn" {
		t.Errorf("trunkInterface() = %q, want hbn", conf.trunkInterface())
	}
	ipamType, err := conf.ipamType()
	if err != nil || ipamType != "host-local" {
		t.Errorf("ipamType() = %q, %v, want host-local, nil", ipamType, err)
	}
}

func TestParseConfigErrors(t *testing.T) {
	tests := map[string]string{
		"missing ipam": `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster"}`,
		"ipam no type": `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{}}`,
		// ipam.type names a binary the plugin executes as root; only address
		// managers are delegated to, not arbitrary chained plugins.
		"ipam not an IPAM plugin": `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-device"}}`,
		"ipam tuning plugin":      `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"tuning"}}`,
		// The ipam block is passed verbatim to the root-run IPAM binary, so
		// options that select host paths or credentials are node-only.
		"host-local dataDir in NAD":              `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-local","dataDir":"/etc/kubernetes"}}`,
		"host-local datadir lower-case in NAD":   `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-local","datadir":"/etc/kubernetes"}}`,
		"host-local resolvConf in NAD":           `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-local","resolvConf":"/etc/shadow"}}`,
		"whereabouts kubeconfig in NAD":          `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"whereabouts","range":"10.0.0.0/24","kubernetes":{"kubeconfig":"/tmp/evil.kubeconfig"}}}`,
		"whereabouts configuration_path in NAD":  `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"whereabouts","range":"10.0.0.0/24","configuration_path":"/tmp/evil.conf"}}`,
		"whereabouts log_file in NAD":            `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"whereabouts","range":"10.0.0.0/24","log_file":"/etc/passwd"}}`,
		"whereabouts LOG_FILE upper-case in NAD": `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"whereabouts","range":"10.0.0.0/24","LOG_FILE":"/etc/passwd"}}`,
		"bad gw v4":                              `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-local"},"linkLocalGateways":{"ipv4":"not-an-ip"}}`,
		"bad gw v6":                              `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-local"},"linkLocalGateways":{"ipv6":"10.0.0.1"}}`,
		// Only link-local gateways: a routable/underlay or node address must not
		// be claimable on the CRA-side port through the attachment config.
		"non-link-local gw v4": `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-local"},"linkLocalGateways":{"ipv4":"10.0.0.1"}}`,
		"non-link-local gw v6": `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-local"},"linkLocalGateways":{"ipv6":"fd00::1"}}`,
		"multicast gw v6":      `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-local"},"linkLocalGateways":{"ipv6":"ff02::1"}}`,
		"invalid json":         `{`,
		// Node-level trust anchors are read from the node config only.
		"agentSocket in NAD":    `{"cniVersion":"1.0.0","type":"cni-workload","ipam":{"type":"host-local"},"agentSocket":"/tmp/evil.sock"}`,
		"craNetns in NAD":       `{"cniVersion":"1.0.0","type":"cni-workload","ipam":{"type":"host-local"},"craNetns":"auto"}`,
		"trunkInterface in NAD": `{"cniVersion":"1.0.0","type":"cni-workload","ipam":{"type":"host-local"},"trunkInterface":"hbn"}`,
	}
	for name, conf := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig([]byte(conf)); err == nil {
				t.Errorf("expected error for %s, got nil", name)
			}
		})
	}
}

func TestParseConfigIPAMOptions(t *testing.T) {
	// Tenant-level IPAM options (ranges, routes, ...) stay accepted; only the
	// node-only ones (see nodeOnlyIPAMKeys) are refused.
	for name, conf := range map[string]string{
		"host-local ranges": `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"host-local","ranges":[[{"subnet":"10.0.0.0/24"}]],"routes":[{"dst":"0.0.0.0/0"}]}}`,
		"whereabouts range": `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"whereabouts","range":"10.0.0.0/24","exclude":["10.0.0.1/32"],"log_level":"debug"}}`,
		"static addresses":  `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"type":"static","addresses":[{"address":"10.0.0.2/24"}]}}`,
		// encoding/json matches field names case-insensitively, so this is how
		// the IPAM plugin itself would read it.
		"mixed-case type": `{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster","ipam":{"Type":"host-local","ranges":[[{"subnet":"10.0.0.0/24"}]]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig([]byte(conf)); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseConfigUnderlay(t *testing.T) {
	// An omitted vrf targets the CRA netns default (underlay) routing table;
	// the agent maps empty/"default"/"main" to the underlay when programming.
	conf := `{"cniVersion":"1.0.0","type":"cni-workload","ipam":{"type":"host-local"}}`
	c, err := parseConfig([]byte(conf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.VRF != "" {
		t.Errorf("VRF = %q, want empty for omitted vrf", c.VRF)
	}
}

func TestGatewayDefaults(t *testing.T) {
	conf, err := parseConfig([]byte(validConf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gw4, err := conf.gatewayV4()
	if err != nil || gw4.String() != defaultLinkLocalV4 {
		t.Errorf("gatewayV4() = %v, %v, want %s", gw4, err, defaultLinkLocalV4)
	}
	gw6, err := conf.gatewayV6()
	if err != nil || gw6.String() != defaultLinkLocalV6 {
		t.Errorf("gatewayV6() = %v, %v, want %s", gw6, err, defaultLinkLocalV6)
	}
}

func TestGatewayOverride(t *testing.T) {
	conf := `{
	  "cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster",
	  "ipam":{"type":"host-local"},
	  "linkLocalGateways":{"ipv4":"169.254.9.9","ipv6":"fe80::abcd"}
	}`
	c, err := parseConfig([]byte(conf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gw4, _ := c.gatewayV4()
	if gw4.String() != "169.254.9.9" {
		t.Errorf("gatewayV4() = %v, want 169.254.9.9", gw4)
	}
	gw6, _ := c.gatewayV6()
	if gw6.String() != "fe80::abcd" {
		t.Errorf("gatewayV6() = %v, want fe80::abcd", gw6)
	}
}

// TestParseConfigReadsNodeConfig checks that the trust anchors come from the
// node config file and are applied to every attachment.
func TestParseConfigReadsNodeConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"agentSocket":"/run/x.sock","craNetns":"cra","trunkInterface":"trunk0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nodeConfigPath = NodeConfigPath })
	nodeConfigPath = path

	conf, err := parseConfig([]byte(validConf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf.AgentSocket != "/run/x.sock" || conf.CRANetns != "cra" || conf.trunkInterface() != "trunk0" {
		t.Errorf("node config not applied: %+v", conf.NodeConfig)
	}
}

func TestLoadNodeConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, perm os.FileMode) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), perm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, perm); err != nil { // WriteFile's mode is subject to the umask
			t.Fatal(err)
		}
		return p
	}

	if cfg, err := loadNodeConfig(filepath.Join(dir, "missing.json")); err != nil || *cfg != (NodeConfig{}) {
		t.Errorf("missing file must yield defaults, got %+v, %v", cfg, err)
	}
	if cfg, err := loadNodeConfig(write("empty.json", " \n", 0o644)); err != nil || *cfg != (NodeConfig{}) {
		t.Errorf("empty file must yield defaults, got %+v, %v", cfg, err)
	}
	if _, err := loadNodeConfig(write("unknown.json", `{"vrf":"x"}`, 0o644)); err == nil {
		t.Error("unknown keys must be rejected")
	}
	if _, err := loadNodeConfig(write("writable.json", `{}`, 0o666)); err == nil {
		t.Error("a group/world-writable node config must be rejected")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(write("target.json", `{}`, 0o644), link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadNodeConfig(link); err == nil {
		t.Error("a symlinked node config must be rejected")
	}
}
