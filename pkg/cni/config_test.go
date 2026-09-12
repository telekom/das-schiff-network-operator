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

	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
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

func TestParseConfigL2Mode(t *testing.T) {
	// L2 mode needs a Layer2 reference and no VRF; gateways are not required and
	// IPAM is optional (the workload is addressed inside the L2 domain).
	conf := `{
	  "cniVersion":"1.0.0","type":"cni-workload",
	  "attachMode":"l2",
	  "layer2AttachmentRef":{"name":"blue"}
	}`
	c, err := parseConfig([]byte(conf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.isL2() {
		t.Errorf("isL2() = false, want true")
	}
	if c.Layer2AttachmentRef == nil || c.Layer2AttachmentRef.Name != "blue" {
		t.Errorf("Layer2AttachmentRef = %+v, want name=blue", c.Layer2AttachmentRef)
	}
}

func TestParseConfigL2Trunk(t *testing.T) {
	// A member without a vlan keeps the domain's own id (resolved by the agent),
	// one with a vlan is translated to that workload-side id.
	conf := `{
	  "cniVersion":"1.0.0","type":"cni-workload",
	  "attachMode":"l2",
	  "layer2Trunk":[{"name":"green"},{"name":"red","vlan":200}]
	}`
	c, err := parseConfig([]byte(conf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Layer2AttachmentRef != nil {
		t.Errorf("Layer2AttachmentRef = %+v, want nil", c.Layer2AttachmentRef)
	}
	if len(c.Layer2Trunk) != 2 {
		t.Fatalf("Layer2Trunk = %+v, want 2 members", c.Layer2Trunk)
	}
	if c.Layer2Trunk[0].Name != "green" || c.Layer2Trunk[0].VLAN != nil {
		t.Errorf("member 0 = %+v, want green with an inherited vlan", c.Layer2Trunk[0])
	}
	if c.Layer2Trunk[1].VLAN == nil || *c.Layer2Trunk[1].VLAN != 200 {
		t.Errorf("member 1 = %+v, want vlan 200", c.Layer2Trunk[1])
	}
}

func TestParseConfigIPAMRequirement(t *testing.T) {
	// IPAM is required for routed attachments, optional in L2 mode.
	if _, err := parseConfig([]byte(
		`{"cniVersion":"1.0.0","type":"cni-workload","vrf":"cluster"}`)); err == nil {
		t.Error("expected an error for a routed attachment without ipam")
	}
	if _, err := parseConfig([]byte(
		`{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2Trunk":[{"name":"green"}]}`,
	)); err != nil {
		t.Errorf("unexpected error for a trunk without ipam: %v", err)
	}
	// The node-only IPAM option check applies in every attach mode that
	// delegates IPAM.
	if _, err := parseConfig([]byte(
		`{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2AttachmentRef":{"name":"blue"},"ipam":{"type":"static","addresses":[{"address":"10.0.0.2/24"}]}}`,
	)); err != nil {
		t.Errorf("unexpected error for an l2 access port with static ipam: %v", err)
	}
	if _, err := parseConfig([]byte(
		`{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2AttachmentRef":{"name":"blue"},"ipam":{"type":"host-local","ranges":[[{"subnet":"10.0.0.0/24"}]],"dataDir":"/tmp/x"}}`,
	)); err == nil {
		t.Error("expected an error for host-local dataDir on an l2 attachment")
	}
}

func TestParseConfigModeErrors(t *testing.T) {
	tests := map[string]string{
		"invalid attach mode":  `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"bogus","ipam":{"type":"host-local"}}`,
		"l2 without ref":       `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","ipam":{"type":"host-local"}}`,
		"l2 with vrf":          `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","vrf":"cluster","layer2AttachmentRef":{"name":"blue"},"ipam":{"type":"host-local"}}`,
		"l2 ref without name":  `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2AttachmentRef":{},"ipam":{"type":"host-local"}}`,
		"ref and trunk":        `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2AttachmentRef":{"name":"blue"},"layer2Trunk":[{"name":"green"}]}`,
		"trunk without l2":     `{"cniVersion":"1.0.0","type":"cni-workload","layer2Trunk":[{"name":"green"}],"ipam":{"type":"host-local"}}`,
		"ref without l2":       `{"cniVersion":"1.0.0","type":"cni-workload","layer2AttachmentRef":{"name":"blue"},"ipam":{"type":"host-local"}}`,
		"trunk with vrf":       `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","vrf":"cluster","layer2Trunk":[{"name":"green"}]}`,
		"trunk with ipam":      `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2Trunk":[{"name":"green"}],"ipam":{"type":"host-local"}}`,
		"trunk member no name": `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2Trunk":[{"vlan":100}]}`,
		"trunk vlan zero":      `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2Trunk":[{"name":"green","vlan":0}]}`,
		"trunk vlan too high":  `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2Trunk":[{"name":"green","vlan":4095}]}`,
		"trunk duplicate ref":  `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2Trunk":[{"name":"green"},{"name":"green","vlan":100}]}`,
		"trunk duplicate vlan": `{"cniVersion":"1.0.0","type":"cni-workload","attachMode":"l2","layer2Trunk":[{"name":"green","vlan":100},{"name":"red","vlan":100}]}`,
		"mtu too small":        `{"cniVersion":"1.0.0","type":"cni-workload","mtu":68,"ipam":{"type":"host-local"}}`,
		"mtu too large":        `{"cniVersion":"1.0.0","type":"cni-workload","mtu":9217,"ipam":{"type":"host-local"}}`,
	}
	for name, conf := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig([]byte(conf)); err == nil {
				t.Errorf("expected error for %s, got nil", name)
			}
		})
	}
}

// TestParseConfigMTU covers the requested MTU being the one the attachment is
// sized with, and an unset one falling back to the shared default the agent
// applies to a request that carries none.
func TestParseConfigMTU(t *testing.T) {
	conf, err := parseConfig([]byte(
		`{"cniVersion":"1.0.0","type":"cni-workload","mtu":9000,"ipam":{"type":"host-local"}}`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if conf.mtu() != 9000 {
		t.Errorf("mtu() = %d, want 9000", conf.mtu())
	}
	if defaultMTU != workloadcni.DefaultPortMTU {
		t.Errorf("defaultMTU = %d, want the agent's %d", defaultMTU, workloadcni.DefaultPortMTU)
	}
}
