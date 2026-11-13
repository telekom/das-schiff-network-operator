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

// Package ipmath provides shared IP/CIDR math used across the intent pipeline.
// It centralises the anycast-gateway derivation so builders (which write the
// IRB config) and the resolver/status path (which surfaces the same addresses
// on resource status) compute identical values.
package ipmath

import (
	"fmt"
	"net/netip"

	"github.com/telekom/t-caas-go-library/pkg/netutil"
)

// GatewayCIDR returns the anycast gateway CIDR for a Network CIDR: the first
// usable host (network address + 1) with the original prefix length preserved,
// e.g. "198.51.100.224/27" → "198.51.100.225/27" and "2001:db8::/64"
// → "2001:db8::1/64".
//
// NOTE: Network resources are expected to carry the *network address* (host
// bits zero) — not an authored host address like "10.0.1.1/24". The
// vnetwork.kb.io webhook enforces this, so we can treat the CIDR's base as the
// network address and derive the gateway as network+1.
func GatewayCIDR(cidr string) (string, error) {
	gw, bits, err := GatewayAddr(cidr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%d", gw.String(), bits), nil
}

// ParseCIDRParts extracts the anycast gateway host IP (network address + 1) and
// the prefix length string from a Network CIDR, e.g. "198.51.100.224/27" →
// ("198.51.100.225", "27"). The gateway is returned without a prefix because
// callers use it as a bare default gateway. On any error (unparseable CIDR or a
// prefix with no usable host, e.g. /32) it returns empty strings so the caller
// skips gateway rendering.
func ParseCIDRParts(cidr string) (gatewayIP, prefixLen string) {
	gw, bits, err := GatewayAddr(cidr)
	if err != nil {
		return "", ""
	}
	return gw.String(), fmt.Sprintf("%d", bits)
}

// GatewayAddr derives the anycast gateway for a Network CIDR, returning the
// gateway address and the original prefix length.
//
// Network resources carry the network address (host bits zero; enforced by the
// vnetwork.kb.io webhook), so the gateway is the first usable host: network
// address + 1. Two edge cases are handled:
//
//   - Point-to-point prefixes (/31 for IPv4, /127 for IPv6, RFC 3021) have no
//     dedicated network/broadcast address; both addresses are usable hosts, so
//     the network address itself is used as the gateway.
//   - Single-host prefixes (/32, /128) — and any case where network+1 would
//     fall outside the prefix or land on the IPv4 broadcast address — have no
//     usable gateway and return an error rather than emitting an invalid CIDR.
func GatewayAddr(cidr string) (netip.Addr, int, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	gw, err := netutil.FirstUsable(prefix)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf(
			"cannot derive gateway for CIDR %q: no usable host address in prefix: %w", cidr, err)
	}
	return gw, prefix.Bits(), nil
}
