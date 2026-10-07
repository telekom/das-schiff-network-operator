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

package ipmath

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGatewayAddr covers the gateway-derivation edge cases: normal subnets use
// the first usable host (network address + 1); point-to-point prefixes (/31,
// /127) use the network address itself; and single-host prefixes (/32, /128)
// have no usable gateway and return an error.
func TestGatewayAddr(t *testing.T) {
	tests := []struct {
		name     string
		cidr     string
		wantCIDR string // expected GatewayCIDR result; "" means expect error
		wantIP   string // expected ParseCIDRParts gateway IP ("" on error)
		wantLen  string // expected ParseCIDRParts prefix length ("" on error)
	}{
		{"ipv4 network address /24", "10.0.1.0/24", "10.0.1.1/24", "10.0.1.1", "24"},
		{"ipv4 network address /27", "198.51.100.224/27", "198.51.100.225/27", "198.51.100.225", "27"},
		{"ipv6 network address /64", "2001:db8::/64", "2001:db8::1/64", "2001:db8::1", "64"},
		{"ipv4 point-to-point /31", "10.0.0.0/31", "10.0.0.0/31", "10.0.0.0", "31"},
		{"ipv6 point-to-point /127", "2001:db8::/127", "2001:db8::/127", "2001:db8::", "127"},
		{"ipv4 single host /32", "10.0.0.5/32", "", "", ""},
		{"ipv6 single host /128", "2001:db8::5/128", "", "", ""},
		{"unparseable", "not-a-cidr", "", "", ""},
		{"ipv4 host bits masked", "198.51.100.230/27", "198.51.100.225/27", "198.51.100.225", "27"},
		{"ipv6 host bits masked", "2001:db8::30/64", "2001:db8::1/64", "2001:db8::1", "64"},
		{"ipv4 default route", "0.0.0.0/0", "0.0.0.1/0", "0.0.0.1", "0"},
		{"ipv6 default route", "::/0", "::1/0", "::1", "0"},
		{"ipv4 upper boundary", "255.255.255.252/30", "255.255.255.253/30", "255.255.255.253", "30"},
		{"ipv6 upper boundary", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffc/126", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffd/126", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffd", "126"},
		{"mapped IPv4 remains IPv6", "::ffff:192.0.2.0/120", "::ffff:192.0.2.1/120", "::ffff:192.0.2.1", "120"},
		{"mapped point-to-point", "::ffff:192.0.2.0/127", "::ffff:192.0.2.0/127", "::ffff:192.0.2.0", "127"},
		{"mapped single host", "::ffff:192.0.2.1/128", "", "", ""},
		{"empty", "", "", "", ""},
		{"invalid prefix length", "192.0.2.0/33", "", "", ""},
		{"zone rejected", "fe80::%eth0/64", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gwCIDR, err := GatewayCIDR(tt.cidr)
			if tt.wantCIDR == "" {
				assert.Error(t, err, "expected error for %q", tt.cidr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantCIDR, gwCIDR)
			}

			ip, plen := ParseCIDRParts(tt.cidr)
			assert.Equal(t, tt.wantIP, ip)
			assert.Equal(t, tt.wantLen, plen)
		})
	}
}
