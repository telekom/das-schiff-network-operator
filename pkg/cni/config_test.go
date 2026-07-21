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

import "testing"

const validConf = `{
  "cniVersion": "1.0.0",
  "name": "routed",
  "type": "cni-routed",
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
		"missing ipam": `{"cniVersion":"1.0.0","type":"cni-routed","vrf":"cluster"}`,
		"ipam no type": `{"cniVersion":"1.0.0","type":"cni-routed","vrf":"cluster","ipam":{}}`,
		"bad gw v4":    `{"cniVersion":"1.0.0","type":"cni-routed","vrf":"cluster","ipam":{"type":"host-local"},"linkLocalGateways":{"ipv4":"not-an-ip"}}`,
		"bad gw v6":    `{"cniVersion":"1.0.0","type":"cni-routed","vrf":"cluster","ipam":{"type":"host-local"},"linkLocalGateways":{"ipv6":"10.0.0.1"}}`,
		"invalid json": `{`,
	}
	for name, conf := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig([]byte(conf)); err == nil {
				t.Errorf("expected error for %s, got nil", name)
			}
		})
	}
}

func TestParseConfigUnderlay(t *testing.T) {
	// An omitted vrf targets the CRA netns default (underlay) routing table;
	// the agent maps empty/"default"/"main" to the underlay when programming.
	conf := `{"cniVersion":"1.0.0","type":"cni-routed","ipam":{"type":"host-local"}}`
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
	  "cniVersion":"1.0.0","type":"cni-routed","vrf":"cluster",
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
