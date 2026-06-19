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

package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

// collectorIsL2GRE reports whether the collector uses L2 (Ethernet) GRE encapsulation.
func collectorIsL2GRE(col *nc.Collector) bool {
	return col.Spec.Protocol == "l2gre"
}

// collectorGREName returns the deterministic, Linux-safe (<=15 char) GRE interface
// name for a Collector. It mirrors the legacy operator's greInterfaceName so both
// reconciler paths produce identical NodeNetworkConfig GRE interfaces.
func collectorGREName(col *nc.Collector) string {
	sum := sha256.Sum256([]byte(col.Name))
	h := hex.EncodeToString(sum[:])[:8]
	if collectorIsL2GRE(col) {
		return "gtap-" + h
	}
	return "gre-" + h
}

// collectorGRELayer maps the Collector protocol to a GRE encapsulation layer.
func collectorGRELayer(col *nc.Collector) networkv1alpha1.GRELayer {
	if collectorIsL2GRE(col) {
		return networkv1alpha1.GRELayer2
	}
	return networkv1alpha1.GRELayer3
}

// collectorGREKey converts the optional Collector GRE key to the uint32 expected by
// the NodeNetworkConfig GRE type.
func collectorGREKey(col *nc.Collector) *uint32 {
	if col.Spec.Key == nil {
		return nil
	}
	key := uint32(*col.Spec.Key) //nolint:gosec // range validated by CRD schema (0..4294967295)
	return &key
}

// mirrorBareIP returns the address without any CIDR suffix.
func mirrorBareIP(addr string) string {
	host, _, _ := strings.Cut(addr, "/")
	return host
}

// mirrorHostAddress returns the bare IP with a single-host prefix (/32 for IPv4,
// /128 for IPv6), matching the legacy operator's loopback/export-filter format.
func mirrorHostAddress(ip string) string {
	if strings.Contains(ip, ":") {
		return ip + "/128"
	}
	return ip + "/32"
}

// appendMirrorSourcePrefix appends a permit entry for the GRE source host address to
// the VRF's EVPN export filter (idempotently), so the per-node loopback /128 is
// advertised into the fabric.
func appendMirrorSourcePrefix(fvrf *networkv1alpha1.FabricVRF, hostAddr string) {
	if fvrf.EVPNExportFilter == nil {
		fvrf.EVPNExportFilter = &networkv1alpha1.Filter{
			DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		}
	}
	for i := range fvrf.EVPNExportFilter.Items {
		if p := fvrf.EVPNExportFilter.Items[i].Matcher.Prefix; p != nil && p.Prefix == hostAddr {
			return
		}
	}
	fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, networkv1alpha1.FilterItem{
		Matcher: networkv1alpha1.Matcher{
			Prefix: &networkv1alpha1.PrefixMatcher{Prefix: hostAddr},
		},
		Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
	})
}

// mirrorDirections expands a TrafficMirror direction into the concrete per-ACL
// directions. The legacy NodeNetworkConfig MirrorACL only supports ingress or
// egress, so "both" (or an empty value) becomes two ACLs.
func mirrorDirections(direction string) []networkv1alpha1.MirrorDirection {
	switch direction {
	case "ingress":
		return []networkv1alpha1.MirrorDirection{networkv1alpha1.MirrorDirectionIngress}
	case "egress":
		return []networkv1alpha1.MirrorDirection{networkv1alpha1.MirrorDirectionEgress}
	default: // "both" or unset
		return []networkv1alpha1.MirrorDirection{
			networkv1alpha1.MirrorDirectionIngress,
			networkv1alpha1.MirrorDirectionEgress,
		}
	}
}
