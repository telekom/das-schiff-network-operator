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

package resolver

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

func ptrStr(s string) *string { return &s }

func testData() *ResolvedData {
	return &ResolvedData{
		Networks: map[string]*ResolvedNetwork{
			"net-a": {Name: "net-a", Spec: nc.NetworkSpec{
				IPv4: &nc.IPNetwork{CIDR: "10.0.0.0/24"},
				IPv6: &nc.IPNetwork{CIDR: "2001:db8::/64"},
			}},
			"net-v4only": {Name: "net-v4only", Spec: nc.NetworkSpec{
				IPv4: &nc.IPNetwork{CIDR: "10.1.0.0/24"},
			}},
		},
		RawDestinations: []nc.Destination{
			{ObjectMeta: metav1.ObjectMeta{Name: "d-m2m", Labels: map[string]string{"type": "gw"}}, Spec: nc.DestinationSpec{VRFRef: ptrStr("vrf-m2m")}},
			{ObjectMeta: metav1.ObjectMeta{Name: "d-c2m", Labels: map[string]string{"type": "gw"}}, Spec: nc.DestinationSpec{VRFRef: ptrStr("vrf-c2m")}},
			{ObjectMeta: metav1.ObjectMeta{Name: "d-dup", Labels: map[string]string{"type": "gw"}}, Spec: nc.DestinationSpec{VRFRef: ptrStr("vrf-m2m")}},   // duplicate vrfRef
			{ObjectMeta: metav1.ObjectMeta{Name: "d-nh", Labels: map[string]string{"type": "gw"}}, Spec: nc.DestinationSpec{NextHop: &nc.NextHopConfig{}}}, // no vrfRef
			{ObjectMeta: metav1.ObjectMeta{Name: "d-other", Labels: map[string]string{"type": "x"}}, Spec: nc.DestinationSpec{VRFRef: ptrStr("vrf-x")}},
		},
		Layer2Attachments: []nc.Layer2Attachment{
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-gw"}, Spec: nc.Layer2AttachmentSpec{
				NetworkRef:   "net-a",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
			}},
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-v4"}, Spec: nc.Layer2AttachmentSpec{
				NetworkRef:   "net-v4only",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
			}},
			{ObjectMeta: metav1.ObjectMeta{Name: "l2a-sel"}, Spec: nc.Layer2AttachmentSpec{
				NetworkRef:   "net-a",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
				NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"rack": "a"}},
			}},
		},
		Nodes: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "node-a1", Labels: map[string]string{"rack": "a"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "node-a2", Labels: map[string]string{"rack": "a"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "node-b1", Labels: map[string]string{"rack": "b"}}},
		},
		Inbounds: []nc.Inbound{
			{ObjectMeta: metav1.ObjectMeta{Name: "ib-gw"}, Spec: nc.InboundSpec{
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "x"}},
			}},
		},
	}
}

func TestNetworkCIDRs(t *testing.T) {
	d := testData()
	if v4, v6 := d.NetworkCIDRs("net-a"); v4 != "10.0.0.0/24" || v6 != "2001:db8::/64" {
		t.Errorf("net-a = %q,%q", v4, v6)
	}
	if v4, v6 := d.NetworkCIDRs("net-v4only"); v4 != "10.1.0.0/24" || v6 != "" {
		t.Errorf("net-v4only = %q,%q; want v6 empty", v4, v6)
	}
	if v4, v6 := d.NetworkCIDRs("missing"); v4 != "" || v6 != "" {
		t.Errorf("missing = %q,%q; want empty", v4, v6)
	}
}

func TestSelectorVRFRefs(t *testing.T) {
	d := testData()
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}}
	got := d.SelectorVRFRefs(sel)
	want := []string{"vrf-c2m", "vrf-m2m"} // sorted, de-duplicated, nextHop skipped
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SelectorVRFRefs = %v, want %v", got, want)
	}
	if got := d.SelectorVRFRefs(nil); got != nil {
		t.Errorf("nil selector = %v, want nil", got)
	}
}

func TestBGPPeeringVRFRefs(t *testing.T) {
	d := testData()

	// listenRange: VRFs come from the referenced Layer2Attachment's destinations.
	listen := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{Ref: nc.BGPPeeringRef{AttachmentRef: ptrStr("l2a-gw")}}}
	if got, want := d.BGPPeeringVRFRefs(listen), []string{"vrf-c2m", "vrf-m2m"}; !reflect.DeepEqual(got, want) {
		t.Errorf("listenRange VRFs = %v, want %v", got, want)
	}

	// loopbackPeer: VRFs come from the referenced Inbounds' destinations.
	loop := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{Ref: nc.BGPPeeringRef{InboundRefs: []string{"ib-gw"}}}}
	if got, want := d.BGPPeeringVRFRefs(loop), []string{"vrf-x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("loopbackPeer VRFs = %v, want %v", got, want)
	}

	// unknown attachment → no VRFs.
	none := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{Ref: nc.BGPPeeringRef{AttachmentRef: ptrStr("nope")}}}
	if got := d.BGPPeeringVRFRefs(none); got != nil {
		t.Errorf("unknown attachment = %v, want nil", got)
	}
}

func TestBGPPeeringLocalIPs(t *testing.T) {
	d := testData()

	// listenRange dual-stack: IRB anycast gateways (network+1) for both families.
	dual := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{
		Mode: nc.BGPPeeringModeListenRange,
		Ref:  nc.BGPPeeringRef{AttachmentRef: ptrStr("l2a-gw")},
	}}
	if got, want := d.BGPPeeringLocalIPs(dual), []string{"10.0.0.1", "2001:db8::1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dual-stack localIPs = %v, want %v", got, want)
	}

	// listenRange v4-only network: single gateway.
	v4 := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{
		Mode: nc.BGPPeeringModeListenRange,
		Ref:  nc.BGPPeeringRef{AttachmentRef: ptrStr("l2a-v4")},
	}}
	if got, want := d.BGPPeeringLocalIPs(v4), []string{"10.1.0.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("v4-only localIPs = %v, want %v", got, want)
	}

	// loopbackPeer mode has no listen-range/IRB → nil.
	loop := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{
		Mode: nc.BGPPeeringModeLoopbackPeer,
		Ref:  nc.BGPPeeringRef{InboundRefs: []string{"ib-gw"}},
	}}
	if got := d.BGPPeeringLocalIPs(loop); got != nil {
		t.Errorf("loopbackPeer localIPs = %v, want nil", got)
	}

	// unresolvable attachment → nil.
	none := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{
		Mode: nc.BGPPeeringModeListenRange,
		Ref:  nc.BGPPeeringRef{AttachmentRef: ptrStr("nope")},
	}}
	if got := d.BGPPeeringLocalIPs(none); got != nil {
		t.Errorf("unknown attachment localIPs = %v, want nil", got)
	}
}

func TestBGPPeeringNodes(t *testing.T) {
	d := testData()

	// listenRange with a node-scoped L2A → only the matching nodes.
	scoped := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{
		Mode: nc.BGPPeeringModeListenRange,
		Ref:  nc.BGPPeeringRef{AttachmentRef: ptrStr("l2a-sel")},
	}}
	if got, want := d.BGPPeeringNodes(scoped), []string{"node-a1", "node-a2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("scoped nodes = %v, want %v", got, want)
	}

	// listenRange with no NodeSelector → all nodes.
	all := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{
		Mode: nc.BGPPeeringModeListenRange,
		Ref:  nc.BGPPeeringRef{AttachmentRef: ptrStr("l2a-gw")},
	}}
	if got, want := d.BGPPeeringNodes(all), []string{"node-a1", "node-a2", "node-b1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("all nodes = %v, want %v", got, want)
	}

	// loopbackPeer is node-independent → all nodes.
	loop := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{
		Mode: nc.BGPPeeringModeLoopbackPeer,
		Ref:  nc.BGPPeeringRef{InboundRefs: []string{"ib-gw"}},
	}}
	if got, want := d.BGPPeeringNodes(loop), []string{"node-a1", "node-a2", "node-b1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("loopbackPeer nodes = %v, want %v", got, want)
	}

	// unresolvable attachment → nil.
	none := &nc.BGPPeering{Spec: nc.BGPPeeringSpec{
		Mode: nc.BGPPeeringModeListenRange,
		Ref:  nc.BGPPeeringRef{AttachmentRef: ptrStr("nope")},
	}}
	if got := d.BGPPeeringNodes(none); got != nil {
		t.Errorf("unknown attachment nodes = %v, want nil", got)
	}
}
