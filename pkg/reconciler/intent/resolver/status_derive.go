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
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/ipmath"
)

// NetworkCIDRs returns the IPv4 and IPv6 CIDRs of the Network referenced by
// name. Each is empty when the Network is unresolved or lacks that family.
// Used to surface the pool CIDRs on consumer resource status (e.g.
// Layer2Attachment.status.networkIPv4/networkIPv6).
func (d *ResolvedData) NetworkCIDRs(networkRef string) (ipv4, ipv6 string) {
	net, ok := d.Networks[networkRef]
	if !ok {
		return "", ""
	}
	if net.Spec.IPv4 != nil {
		ipv4 = net.Spec.IPv4.CIDR
	}
	if net.Spec.IPv6 != nil {
		ipv6 = net.Spec.IPv6.CIDR
	}
	return ipv4, ipv6
}

// SelectorVRFRefs returns the sorted, de-duplicated VRF names (vrfRef) of every
// Destination matched by sel that carries a vrfRef. This is the VRF list a
// consumer with a destinations selector (Layer2Attachment, Inbound, Outbound)
// is plumbed into. Destinations that use nextHop (no vrfRef) are skipped.
func (d *ResolvedData) SelectorVRFRefs(sel *metav1.LabelSelector) []string {
	if sel == nil {
		return nil
	}
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return nil
	}
	set := map[string]struct{}{}
	for i := range d.RawDestinations {
		rd := &d.RawDestinations[i]
		if !selector.Matches(labels.Set(rd.Labels)) {
			continue
		}
		if rd.Spec.VRFRef == nil || *rd.Spec.VRFRef == "" {
			continue
		}
		set[*rd.Spec.VRFRef] = struct{}{}
	}
	return sortedStringSet(set)
}

// attachmentVRFRefs returns the VRF names reachable via the referenced
// Layer2Attachment's destinations. Used for BGPPeering listenRange mode, where
// the VRFs come from the referenced L2A (the transfer network segment).
func (d *ResolvedData) attachmentVRFRefs(attachmentRef string) []string {
	for i := range d.Layer2Attachments {
		l2a := &d.Layer2Attachments[i]
		if l2a.Name == attachmentRef {
			return d.SelectorVRFRefs(l2a.Spec.Destinations)
		}
	}
	return nil
}

// inboundVRFRefs returns the VRF names reachable via the referenced Inbound's
// destinations. Used for BGPPeering loopbackPeer mode.
func (d *ResolvedData) inboundVRFRefs(inboundRef string) []string {
	for i := range d.Inbounds {
		ib := &d.Inbounds[i]
		if ib.Name == inboundRef {
			return d.SelectorVRFRefs(ib.Spec.Destinations)
		}
	}
	return nil
}

// BGPPeeringVRFRefs returns the sorted, de-duplicated VRF names a BGPPeering
// relates to. In listenRange mode the VRFs come from the referenced
// Layer2Attachment (ref.attachmentRef); in loopbackPeer mode they come from the
// referenced Inbounds (ref.inboundRefs).
func (d *ResolvedData) BGPPeeringVRFRefs(bp *nc.BGPPeering) []string {
	set := map[string]struct{}{}
	if bp.Spec.Ref.AttachmentRef != nil {
		for _, v := range d.attachmentVRFRefs(*bp.Spec.Ref.AttachmentRef) {
			set[v] = struct{}{}
		}
	}
	for _, ref := range bp.Spec.Ref.InboundRefs {
		for _, v := range d.inboundVRFRefs(ref) {
			set[v] = struct{}{}
		}
	}
	return sortedStringSet(set)
}

// BGPPeeringLocalIPs returns the local peering IP addresses for a BGPPeering.
// For listenRange mode these are the IRB anycast gateway addresses the node
// listens on: the first usable host of the referenced Layer2Attachment's
// Network CIDR, one per address family, as bare IPs (no prefix). This is the
// same gateway the L2A builder installs as IRB.IPAddresses (which keeps the
// prefix). Returns nil for other modes or when the attachment/Network cannot be
// resolved.
func (d *ResolvedData) BGPPeeringLocalIPs(bp *nc.BGPPeering) []string {
	if bp.Spec.Mode != nc.BGPPeeringModeListenRange || bp.Spec.Ref.AttachmentRef == nil {
		return nil
	}

	var l2a *nc.Layer2Attachment
	for i := range d.Layer2Attachments {
		if d.Layer2Attachments[i].Name == *bp.Spec.Ref.AttachmentRef {
			l2a = &d.Layer2Attachments[i]
			break
		}
	}
	if l2a == nil {
		return nil
	}

	net, ok := d.Networks[l2a.Spec.NetworkRef]
	if !ok {
		return nil
	}

	var ips []string
	if net.Spec.IPv4 != nil && net.Spec.IPv4.CIDR != "" {
		if ip, _ := ipmath.ParseCIDRParts(net.Spec.IPv4.CIDR); ip != "" {
			ips = append(ips, ip)
		}
	}
	if net.Spec.IPv6 != nil && net.Spec.IPv6.CIDR != "" {
		if ip, _ := ipmath.ParseCIDRParts(net.Spec.IPv6.CIDR); ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips
}

// BGPPeeringNodes returns the names of the nodes a BGPPeering's configuration
// lands on. In listenRange mode these are the nodes selected by the referenced
// Layer2Attachment's NodeSelector (all nodes when the L2A has no selector). In
// loopbackPeer mode the peering is node-independent (ClusterVRF), so all nodes
// apply. Returns nil when the referenced Layer2Attachment cannot be resolved.
func (d *ResolvedData) BGPPeeringNodes(bp *nc.BGPPeering) []string {
	var selector *metav1.LabelSelector
	if bp.Spec.Mode == nc.BGPPeeringModeListenRange {
		if bp.Spec.Ref.AttachmentRef == nil {
			return nil
		}
		var l2a *nc.Layer2Attachment
		for i := range d.Layer2Attachments {
			if d.Layer2Attachments[i].Name == *bp.Spec.Ref.AttachmentRef {
				l2a = &d.Layer2Attachments[i]
				break
			}
		}
		if l2a == nil {
			return nil
		}
		selector = l2a.Spec.NodeSelector
	}
	// loopbackPeer (and listenRange with a nil NodeSelector) applies to all nodes.
	return d.matchNodeNames(selector)
}

// matchNodeNames returns the names of nodes matching selector. A nil selector
// matches all nodes (mirrors the builder's matchNodes; note that
// LabelSelectorAsSelector(nil) matches nothing, which is not what we want here).
func (d *ResolvedData) matchNodeNames(selector *metav1.LabelSelector) []string {
	sel := labels.Everything()
	if selector != nil {
		var err error
		sel, err = metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return nil
		}
	}
	var names []string
	for i := range d.Nodes {
		if sel.Matches(labels.Set(d.Nodes[i].Labels)) {
			names = append(names, d.Nodes[i].Name)
		}
	}
	return names
}

// sortedStringSet returns the keys of set as a sorted slice, or nil when empty
// (so an omitempty status field stays absent rather than an empty array).
func sortedStringSet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
